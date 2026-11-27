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

	"daed-next/internal/model"
)

const releaseAPI = "https://api.github.com/repos/daeuniverse/dae/releases/latest"
const daemonInit = "/etc/init.d/dae"
const coreMaxSize = 96 << 20

var stableCoreVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

type CoreRelease struct {
	Installed   bool   `json:"installed"`
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	URL         string `json:"url"`
	Available   bool   `json:"available"`
	Installable bool   `json:"installable"`
	assetURL    string
	digest      string
}

func (m *Manager) targetInstallDaeBin() (string, bool) {
	if binPath, found := m.resolveDaeBin(); found {
		return binPath, true
	}
	m.mu.Lock()
	configured := strings.TrimSpace(m.daeBin)
	m.mu.Unlock()
	if configured == "" || configured == "dae" || configured == "/usr/bin/dae" {
		return "/usr/bin/dae", false
	}
	if configured == "/usr/local/bin/dae" {
		return "/usr/local/bin/dae", false
	}
	return configured, false
}

func (m *Manager) CheckCore(ctx context.Context) (CoreRelease, error) {
	targetBin, found := m.targetInstallDaeBin()
	current := "未安装"
	if found {
		out, err := runCommand(5*time.Second, targetBin, "--version")
		if err != nil {
			return CoreRelease{}, fmt.Errorf("读取 dae 版本失败: %w", err)
		}
		if _, err := fmt.Sscanf(string(out), "dae version %s", &current); err != nil {
			return CoreRelease{}, fmt.Errorf("无法识别当前 dae 版本")
		}
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
	result := CoreRelease{
		Installed: found,
		Current:   current,
		Latest:    release.Tag,
		URL:       base + "tag/" + release.Tag,
	}
	if !found {
		result.Available = true
	} else if stableCoreVersion.MatchString(current) {
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

	allowedPath := targetBin == "/usr/bin/dae" || targetBin == "/usr/local/bin/dae"
	result.Installable = result.Available && result.assetURL != "" && !m.mockMode &&
		allowedPath && (!found || !isPackagedCore())
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
	if !release.Installable || (version != "" && version != release.Latest) {
		return fmt.Errorf("该版本当前不可安装，请重新检查")
	}
	targetVersion := release.Latest
	targetBin, hadPrevious := m.targetInstallDaeBin()
	if err := os.MkdirAll(filepath.Dir(targetBin), 0o755); err != nil {
		return err
	}

	// Stage the compressed archive in RAM (/tmp) so flash storage on embedded routers is not exhausted.
	dlDir, err := os.MkdirTemp("", "daestudio-core-dl-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dlDir)

	dir, err := os.MkdirTemp(filepath.Dir(targetBin), ".daestudio-core-")
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
	if err != nil || installedVersion != targetVersion {
		return fmt.Errorf("下载的内核无法运行或版本不符")
	}

	// Ensure geoip.dat and geosite.dat exist before running `dae validate -c config.dae` on fresh installs!
	geoSt := m.InspectGeoData(model.DefaultGeoDataConfig())
	if !geoSt.GeoIP.Exists || !geoSt.GeoSite.Exists {
		m.appendLog("info", "检测到缺失 GeoIP / GeoSite 分流数据库，正在自动下载补齐…")
		if _, geoErr := m.UpdateGeoData(ctx, model.DefaultGeoDataConfig(), "all"); geoErr != nil {
			return fmt.Errorf("自动补齐 geoip.dat / geosite.dat 失败: %w", geoErr)
		}
	}

	if _, cfgErr := os.Stat(m.configPath); cfgErr == nil {
		if valOut, valErr := runCommand(30*time.Second, candidate, "validate", "-c", m.configPath); valErr != nil {
			return fmt.Errorf("新版内核未通过现有配置校验: %w %s", valErr, strings.TrimSpace(string(valOut)))
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	backup := targetBin + ".daestudio-previous"
	if hadPrevious {
		previous, openErr := os.Open(targetBin)
		if openErr != nil {
			return openErr
		}
		backupStage := filepath.Join(dir, "previous")
		writeErr := writeCoreFile(backupStage, previous, 0o755)
		previous.Close()
		if writeErr != nil {
			return writeErr
		}
		if err := os.Rename(backupStage, backup); err != nil {
			return err
		}
	}

	wasRunning := detectDaePID() > 0
	if err := os.Rename(candidate, targetBin); err != nil {
		return err
	}
	m.mu.Lock()
	m.daeBin = targetBin
	m.mu.Unlock()

	if svcErr := m.EnsureServiceScripts(targetBin); svcErr != nil {
		m.appendLog("warn", svcErr.Error())
	}

	if !wasRunning {
		m.appendLog("info", fmt.Sprintf("已安装 dae %s 至 %s", targetVersion, targetBin))
		return nil
	}
	if err := m.restartUpdatedCore(targetBin); err != nil {
		if hadPrevious {
			if rollbackErr := os.Rename(backup, targetBin); rollbackErr != nil {
				return fmt.Errorf("新版启动失败，恢复旧文件失败: %w", rollbackErr)
			}
			if rollbackErr := m.restartUpdatedCore(targetBin); rollbackErr != nil {
				return fmt.Errorf("已恢复旧文件，服务启动失败: %w", rollbackErr)
			}
			return fmt.Errorf("新版启动失败，已恢复旧内核: %w", err)
		}
		return fmt.Errorf("内核启动失败: %w", err)
	}
	m.appendLog("info", fmt.Sprintf("已升级并重启 dae %s", targetVersion))
	return nil
}

// BootstrapEnvironment ensures service scripts, dae core binary, and geoip.dat/geosite.dat are all ready.
// Invoked by `daed-next -bootstrap-env` during package installation.
func (m *Manager) BootstrapEnvironment(ctx context.Context) error {
	if m.mockMode || runtime.GOOS != "linux" {
		return nil
	}
	targetBin, found := m.targetInstallDaeBin()
	if err := m.EnsureServiceScripts(targetBin); err != nil {
		fmt.Printf("[自检] 提示: %v\n", err)
	}
	if !found {
		fmt.Printf("[自检] 未检测到 %s 内核，正在从官方仓库下载稳定版 dae 内核...\n", targetBin)
		rel, err := m.CheckCore(ctx)
		if err != nil {
			return fmt.Errorf("查询官方 dae 发行版失败: %w", err)
		}
		if !rel.Installable {
			return fmt.Errorf("当前架构暂不支持自动下载 dae 内核")
		}
		if err := m.InstallCore(ctx, rel.Latest); err != nil {
			return err
		}
		fmt.Printf("[自检] 已成功安装 dae %s -> %s\n", rel.Latest, targetBin)
	} else {
		fmt.Printf("[自检] 已检测到 dae 内核 (%s)\n", targetBin)
	}

	geoSt := m.InspectGeoData(model.DefaultGeoDataConfig())
	if !geoSt.GeoIP.Exists || !geoSt.GeoSite.Exists {
		fmt.Printf("[自检] 正在自动下载并校验 geoip.dat / geosite.dat 分流数据库...\n")
		if _, err := m.UpdateGeoData(ctx, model.DefaultGeoDataConfig(), "all"); err != nil {
			return fmt.Errorf("下载分流数据库失败: %w", err)
		}
		fmt.Printf("[自检] geoip.dat / geosite.dat 分流数据库已就绪\n")
	} else {
		fmt.Printf("[自检] 分流数据库已就绪 (%s)\n", geoSt.Dir)
	}
	return nil
}

func (m *Manager) restartUpdatedCore(targetBin string) error {
	if _, err := os.Stat(daemonInit); err == nil {
		if _, err := runCommand(15*time.Second, daemonInit, "restart"); err != nil {
			return err
		}
	} else if !isOpenWrtHost() && hasSystemdDaeUnit() {
		if _, err := runCommand(15*time.Second, "systemctl", "restart", "dae"); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("未检测到系统服务管理脚本")
	}

	target, err := os.Stat(targetBin)
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
