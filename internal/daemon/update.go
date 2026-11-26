package daemon

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const releaseAPI = "https://api.github.com/repos/daeuniverse/dae/releases/latest"
const daemonInit = "/etc/init.d/dae"
const coreMaxSize = 96 << 20

var stableCoreVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

type CoreRelease struct {
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	URL         string `json:"url"`
	Available   bool   `json:"available"`
	Installable bool   `json:"installable"`
	assetURL    string
	digest      string
}

func (m *Manager) CheckCore(ctx context.Context) (CoreRelease, error) {
	out, err := runCommand(5*time.Second, m.daeBin, "--version")
	if err != nil {
		return CoreRelease{}, fmt.Errorf("读取 dae 版本失败: %w", err)
	}
	var current string
	if _, err := fmt.Sscanf(string(out), "dae version %s", &current); err != nil {
		return CoreRelease{}, fmt.Errorf("无法识别当前 dae 版本")
	}
	var release struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	checkCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	body, err := fetchCore(checkCtx, releaseAPI)
	if err != nil {
		return CoreRelease{}, err
	}
	defer body.Close()
	if err := json.NewDecoder(io.LimitReader(body, 2<<20)).Decode(&release); err != nil {
		return CoreRelease{}, fmt.Errorf("读取官方发行信息: %w", err)
	}
	if !stableCoreVersion.MatchString(release.Tag) {
		return CoreRelease{}, fmt.Errorf("无法识别官方稳定版版本号")
	}
	base := "https://github.com/daeuniverse/dae/releases/"
	result := CoreRelease{Current: current, Latest: release.Tag, URL: base + "tag/" + release.Tag}
	if stableCoreVersion.MatchString(current) {
		var a, b [3]int
		fmt.Sscanf(current, "v%d.%d.%d", &a[0], &a[1], &a[2])
		fmt.Sscanf(release.Tag, "v%d.%d.%d", &b[0], &b[1], &b[2])
		for i := range a {
			if a[i] != b[i] {
				result.Available = b[i] > a[i]
				break
			}
		}
	}
	assetName := coreAssetName()
	for _, asset := range release.Assets {
		if asset.Name == assetName && strings.HasPrefix(asset.Digest, "sha256:") {
			digest := strings.TrimPrefix(asset.Digest, "sha256:")
			if raw, err := hex.DecodeString(digest); err == nil && len(raw) == sha256.Size {
				result.digest = digest
				result.assetURL = base + "download/" + release.Tag + "/" + assetName
			}
			break
		}
	}
	_, initErr := os.Stat(daemonInit)
	result.Installable = result.Available && result.assetURL != "" && !m.mockMode &&
		m.daeBin == "/usr/bin/dae" && initErr == nil && !isPackagedCore()
	return result, nil
}

func isPackagedCore() bool {
	if opkgPath, err := exec.LookPath("opkg"); err == nil {
		out, cmdErr := runCommand(3*time.Second, opkgPath, "status", "dae")
		if cmdErr == nil && strings.Contains(string(out), " installed") {
			return true
		}
	}
	if apkPath, err := exec.LookPath("apk"); err == nil {
		if _, cmdErr := runCommand(3*time.Second, apkPath, "info", "-e", "dae"); cmdErr == nil {
			return true
		}
	}
	return false
}

func fetchCore(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	return fetchGitHubRelease(ctx, rawURL)
}

func (m *Manager) InstallCore(ctx context.Context, version string) error {
	release, err := m.CheckCore(ctx)
	if err != nil {
		return err
	}
	if !release.Installable || version != release.Latest {
		return fmt.Errorf("该版本当前不可安装，请重新检查")
	}
	// Stage the compressed archive in RAM (/tmp) so flash storage on embedded routers is not exhausted.
	dlDir, err := os.MkdirTemp("", "daestudio-core-dl-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dlDir)

	dir, err := os.MkdirTemp(filepath.Dir(m.daeBin), ".daestudio-core-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	candidate := filepath.Join(dir, "dae")
	if err := stageCore(ctx, release, filepath.Join(dlDir, "release.zip"), candidate); err != nil {
		return err
	}
	out, err := runCommand(8*time.Second, candidate, "--version")
	var installedVersion string
	fmt.Sscanf(string(out), "dae version %s", &installedVersion)
	if err != nil || installedVersion != version {
		return fmt.Errorf("下载的内核无法运行或版本不符")
	}
	if _, err := runCommand(30*time.Second, candidate, "validate", "-c", m.configPath); err != nil {
		return fmt.Errorf("新版内核未通过现有配置校验: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	previous, err := os.Open(m.daeBin)
	if err != nil {
		return err
	}
	defer previous.Close()
	backup := m.daeBin + ".daestudio-previous"
	backupStage := filepath.Join(dir, "previous")
	if err := writeCoreFile(backupStage, previous, 0o755); err != nil {
		return err
	}
	if err := os.Rename(backupStage, backup); err != nil {
		return err
	}
	wasRunning := detectDaePID() > 0
	if err := os.Rename(candidate, m.daeBin); err != nil {
		return err
	}
	if !wasRunning {
		return nil
	}
	if err := m.restartUpdatedCore(); err != nil {
		if rollbackErr := os.Rename(backup, m.daeBin); rollbackErr != nil {
			return fmt.Errorf("新版启动失败，恢复旧文件失败: %w", rollbackErr)
		}
		if rollbackErr := m.restartUpdatedCore(); rollbackErr != nil {
			return fmt.Errorf("已恢复旧文件，服务启动失败: %w", rollbackErr)
		}
		return fmt.Errorf("新版启动失败，已恢复旧内核: %w", err)
	}
	return nil
}

func (m *Manager) restartUpdatedCore() error {
	if _, err := runCommand(15*time.Second, daemonInit, "restart"); err != nil {
		return err
	}
	target, err := os.Stat(m.daeBin)
	if err != nil {
		return err
	}
	var lastPID int
	for i := 0; i < 10; i++ {
		time.Sleep(time.Second)
		pid := detectDaePID()
		proc, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
		if err == nil && os.SameFile(target, proc) {
			if lastPID == pid {
				return nil
			}
			lastPID = pid
		} else {
			lastPID = 0
		}
	}
	return fmt.Errorf("未检测到更新后的 dae 进程")
}

func stageCore(ctx context.Context, release CoreRelease, archive, candidate string) error {
	body, err := fetchCore(ctx, release.assetURL)
	if err != nil {
		return err
	}
	defer body.Close()
	digest := sha256.New()
	if err := writeCoreFile(archive, io.TeeReader(body, digest), 0o600); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != release.digest {
		return fmt.Errorf("发行包 SHA-256 校验失败")
	}
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, entry := range z.File {
		name := filepath.Base(entry.Name)
		if (name != "dae" && name != strings.TrimSuffix(coreAssetName(), ".zip")) || !entry.Mode().IsRegular() {
			continue
		}
		r, err := entry.Open()
		if err != nil {
			return err
		}
		writeErr := writeCoreFile(candidate, r, 0o755)
		closeErr := r.Close()
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	return fmt.Errorf("发行包中未找到 dae 可执行文件")
}

func writeCoreFile(path string, r io.Reader, mode os.FileMode) error {
	return writeSyncedFile(path, r, mode, coreMaxSize)
}

func coreAssetName() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	switch runtime.GOARCH {
	case "arm64":
		return "dae-linux-arm64.zip"
	case "amd64":
		return "dae-linux-x86_64.zip"
	case "386":
		return "dae-linux-x86_32.zip"
	}
	return ""
}
