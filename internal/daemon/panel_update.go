package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// PanelVersion and DefaultUpdateRepo can be overridden at link time via -ldflags "-X ...".
var (
	PanelVersion      = "v1.0.9"
	DefaultUpdateRepo = ""
)

const (
	panelMaxSize    = 64 << 20
	panelInitScript = "/etc/init.d/daed-next"
)

var (
	errRepoUnconfigured = errors.New("repo unconfigured")
	validGitHubRepo     = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// ErrRepoUnconfigured reports whether err indicates that no GitHub release repository is configured.
func ErrRepoUnconfigured(err error) bool {
	return errors.Is(err, errRepoUnconfigured)
}

type PanelRelease struct {
	Current       string `json:"current"`
	Latest        string `json:"latest"`
	Repo          string `json:"repo"`
	URL           string `json:"url"`
	PublishedAt   string `json:"publishedAt,omitempty"`
	Available     bool   `json:"available"`
	Installable   bool   `json:"installable"`
	AssetName     string `json:"assetName,omitempty"`
	InstallReason string `json:"installReason,omitempty"`
	assetURL      string
	sumsURL       string
	digest        string
}

// NormalizeGitHubRepo extracts and validates an "owner/repo" slug (or empty string to clear).
func NormalizeGitHubRepo(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil {
			return "", fmt.Errorf("请使用 HTTPS GitHub 仓库地址")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 {
			s = parts[0] + "/" + parts[1]
		}
	} else {
		s = strings.TrimSuffix(s, "/")
	}
	s = strings.TrimSuffix(s, ".git")
	if len(s) > 120 || !validGitHubRepo.MatchString(s) {
		return "", fmt.Errorf("仓库地址格式无效，请使用 owner/repo（例如 yourname/daed-next）")
	}
	owner, repo, _ := strings.Cut(s, "/")
	if owner == "." || owner == ".." || repo == "." || repo == ".." {
		return "", fmt.Errorf("仓库地址格式无效")
	}
	return s, nil
}

// EffectiveUpdateRepo resolves the active GitHub repository from state, environment, or build default.
func EffectiveUpdateRepo(stateRepo string) string {
	const previousOfficialRepo = "skylink9119/skylink9119.github.io"
	const officialRepo = "skylink9119/DaeStudio"
	if repo, err := NormalizeGitHubRepo(stateRepo); err == nil &&
		strings.EqualFold(repo, previousOfficialRepo) && strings.EqualFold(DefaultUpdateRepo, officialRepo) {
		stateRepo = ""
	}
	for _, candidate := range []string{stateRepo, os.Getenv("DAED_NEXT_REPO"), DefaultUpdateRepo} {
		if norm, err := NormalizeGitHubRepo(candidate); err == nil && norm != "" {
			return norm
		}
	}
	return ""
}

func compareSemver(current, latest string) bool {
	if !stableCoreVersion.MatchString(current) || !stableCoreVersion.MatchString(latest) {
		return false
	}
	var a, b [3]int
	fmt.Sscanf(current, "v%d.%d.%d", &a[0], &a[1], &a[2])
	fmt.Sscanf(latest, "v%d.%d.%d", &b[0], &b[1], &b[2])
	for i := range a {
		if a[i] != b[i] {
			return b[i] > a[i]
		}
	}
	return false
}

func panelAssetName() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	switch runtime.GOARCH {
	case "arm64":
		return "daed-next-linux-arm64"
	case "amd64":
		return "daed-next-linux-amd64"
	case "386":
		return "daed-next-linux-386"
	case "mipsle":
		return "daed-next-openwrt-mipsle"
	}
	return ""
}

func resolveSelfExecutable() (string, bool) {
	if runtime.GOOS != "linux" {
		return "", false
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "", false
	}
	if realExe, err := filepath.EvalSymlinks(exe); err == nil && realExe != "" {
		exe = realExe
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	if exe != "/usr/bin/daed-next" && exe != "/usr/local/bin/daed-next" {
		return "", false
	}
	st, err := os.Stat(exe)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	return exe, true
}

func fetchGitHubRelease(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "DaeStudio/"+PanelVersion)
	if strings.EqualFold(req.URL.Hostname(), "api.github.com") {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	transport := &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: true,
	}
	client := &http.Client{
		Timeout:   90 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			host := req.URL.Hostname()
			if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil ||
				(req.URL.Port() != "" && req.URL.Port() != "443") ||
				(host != "github.com" && host != "api.github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com") {
				return fmt.Errorf("GitHub 下载重定向地址不受信任")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("访问 GitHub 发行服务失败: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("未找到该仓库的正式版 Release（请确认仓库公开且已发布 v* 标签版本）")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("GitHub 返回 HTTP %d", resp.StatusCode)
	}
	return &closingBody{ReadCloser: resp.Body, transport: transport}, nil
}

type closingBody struct {
	io.ReadCloser
	transport *http.Transport
}

func (c *closingBody) Close() error {
	err := c.ReadCloser.Close()
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	return err
}

type githubPanelRelease struct {
	Tag         string `json:"tag_name"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	Assets      []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

func latestPanelRelease(releases []githubPanelRelease) (githubPanelRelease, string) {
	var selected githubPanelRelease
	var version string
	for _, release := range releases {
		candidate := strings.TrimPrefix(release.Tag, "DaeStudio-")
		if release.Draft || release.Prerelease || !stableCoreVersion.MatchString(candidate) {
			continue
		}
		hasBinary := false
		for _, asset := range release.Assets {
			switch asset.Name {
			case "daed-next-linux-arm64", "daed-next-linux-amd64", "daed-next-linux-386", "daed-next-openwrt-mipsle":
				hasBinary = true
			}
		}
		if hasBinary && (version == "" || compareSemver(version, candidate)) {
			selected, version = release, candidate
		}
	}
	return selected, version
}

func (m *Manager) CheckPanel(ctx context.Context, repo string) (PanelRelease, error) {
	effectiveRepo := EffectiveUpdateRepo(repo)
	if effectiveRepo == "" {
		return PanelRelease{}, fmt.Errorf("%w: 请先填写面板 GitHub 仓库地址（格式：owner/repo）", errRepoUnconfigured)
	}

	apiURL := "https://api.github.com/repos/" + effectiveRepo + "/releases?per_page=30"
	checkCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()

	body, err := fetchGitHubRelease(checkCtx, apiURL)
	if err != nil {
		return PanelRelease{}, err
	}
	defer body.Close()

	var releases []githubPanelRelease
	if err := json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&releases); err != nil {
		return PanelRelease{}, fmt.Errorf("解析面板发行信息失败: %w", err)
	}
	release, version := latestPanelRelease(releases)
	if version == "" {
		return PanelRelease{}, fmt.Errorf("该仓库最近 30 个 Release 中没有找到 DaeStudio 正式版")
	}

	base := "https://github.com/" + effectiveRepo + "/releases/"
	assetName := panelAssetName()
	out := PanelRelease{
		Current:     PanelVersion,
		Latest:      version,
		Repo:        effectiveRepo,
		URL:         base + "tag/" + release.Tag,
		PublishedAt: release.PublishedAt,
		Available:   compareSemver(PanelVersion, version),
		AssetName:   assetName,
	}

	for _, asset := range release.Assets {
		if assetName != "" && asset.Name == assetName {
			out.assetURL = base + "download/" + release.Tag + "/" + assetName
			if strings.HasPrefix(asset.Digest, "sha256:") {
				d := strings.TrimPrefix(asset.Digest, "sha256:")
				if raw, decErr := hex.DecodeString(d); decErr == nil && len(raw) == sha256.Size {
					out.digest = strings.ToLower(d)
				}
			}
		} else if asset.Name == "SHA256SUMS" {
			out.sumsURL = base + "download/" + release.Tag + "/SHA256SUMS"
		}
	}

	exe, hasValidExe := resolveSelfExecutable()
	if hasValidExe && out.Available && !m.mockMode {
		_, err := m.panelRestart(exe)
		hasValidExe = err == nil
		if err != nil {
			out.InstallReason = err.Error()
		}
	}
	hasChecksum := out.sumsURL != "" && out.digest != ""
	out.Installable = out.Available && !m.mockMode && hasValidExe && out.assetURL != "" && hasChecksum
	if out.Available && !out.Installable && out.InstallReason == "" {
		switch {
		case m.mockMode:
			out.InstallReason = "预览环境不支持在线升级"
		case !hasValidExe:
			out.InstallReason = "无法确认当前可执行文件路径，请使用安装包升级"
		case out.assetURL == "":
			out.InstallReason = "发行版缺少当前架构的面板二进制"
		case !hasChecksum:
			out.InstallReason = "发行版缺少 SHA256SUMS 或 GitHub SHA-256 摘要"
		}
	}
	return out, nil
}

func parseSHA256Sums(content, targetAsset string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	var digest string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		hashHex := strings.ToLower(strings.TrimSpace(fields[0]))
		fileName := strings.TrimPrefix(strings.TrimSpace(fields[1]), "*")
		if fileName == targetAsset {
			raw, err := hex.DecodeString(hashHex)
			if err != nil || len(raw) != sha256.Size {
				return "", fmt.Errorf("SHA256SUMS 中校验值格式无效")
			}
			if digest != "" {
				return "", fmt.Errorf("SHA256SUMS 中存在重复校验记录")
			}
			digest = hashHex
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if digest != "" {
		return digest, nil
	}
	return "", fmt.Errorf("SHA256SUMS 中未找到 %s 的校验记录", targetAsset)
}

func writeSyncedFile(path string, r io.Reader, mode os.FileMode, maxBytes int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(r, maxBytes+1))
	if copyErr != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return copyErr
	}
	if n > maxBytes {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("下载文件超出大小限制")
	}
	if syncErr := f.Sync(); syncErr != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return syncErr
	}
	if closeErr := f.Close(); closeErr != nil {
		_ = os.Remove(path)
		return closeErr
	}
	return nil
}

// StagePanelUpdate downloads, SHA-256 verifies, self-tests, and atomically replaces the running daed-next binary.
// It returns a function that triggers a background service restart after the HTTP response has been sent.
func (m *Manager) StagePanelUpdate(ctx context.Context, repo, version string) (func() error, error) {
	release, err := m.CheckPanel(ctx, repo)
	if err != nil {
		return nil, err
	}
	if !release.Installable || version != release.Latest {
		return nil, fmt.Errorf("该面板版本当前不可在线安装，请重新检测")
	}
	exePath, ok := resolveSelfExecutable()
	if !ok {
		return nil, fmt.Errorf("无法定位当前面板可执行文件路径")
	}

	restart, err := m.panelRestart(exePath)
	if err != nil {
		return nil, err
	}
	sumsCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sumsBody, err := fetchGitHubRelease(sumsCtx, release.sumsURL)
	if err != nil {
		return nil, fmt.Errorf("下载 SHA256SUMS 失败: %w", err)
	}
	sumsRaw, err := io.ReadAll(io.LimitReader(sumsBody, (64<<10)+1))
	sumsBody.Close()
	if err != nil || len(sumsRaw) > 64<<10 {
		return nil, fmt.Errorf("读取 SHA256SUMS 失败或超过大小限制")
	}
	expectedDigest, err := parseSHA256Sums(string(sumsRaw), release.AssetName)
	if err != nil {
		return nil, err
	}
	if release.digest != expectedDigest {
		return nil, fmt.Errorf("GitHub Release 摘要与 SHA256SUMS 不一致，已中止升级")
	}

	stageDir, err := os.MkdirTemp(filepath.Dir(exePath), ".daestudio-panel-")
	if err != nil {
		return nil, fmt.Errorf("创建升级临时目录失败: %w", err)
	}
	defer os.RemoveAll(stageDir)

	candidate := filepath.Join(stageDir, "daed-next.new")
	dlBody, err := fetchGitHubRelease(ctx, release.assetURL)
	if err != nil {
		return nil, err
	}
	hasher := sha256.New()
	writeErr := writeSyncedFile(candidate, io.TeeReader(dlBody, hasher), 0o755, panelMaxSize)
	dlBody.Close()
	if writeErr != nil {
		return nil, writeErr
	}

	actualDigest := hex.EncodeToString(hasher.Sum(nil))
	if actualDigest != expectedDigest {
		return nil, fmt.Errorf("面板安装包 SHA-256 校验未通过")
	}

	if err := verifyPanelCandidate(candidate, version); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	prevFile, err := os.Open(exePath)
	if err != nil {
		return nil, err
	}
	backupStage := filepath.Join(stageDir, "daed-next.prev")
	backupErr := writeSyncedFile(backupStage, prevFile, 0o755, panelMaxSize)
	prevFile.Close()
	if backupErr != nil {
		return nil, backupErr
	}
	backupPath := exePath + ".daestudio-previous"
	if err := os.Rename(backupStage, backupPath); err != nil {
		return nil, err
	}

	if err := os.Rename(candidate, exePath); err != nil {
		return nil, fmt.Errorf("替换面板可执行文件失败: %w", err)
	}

	dir, err := os.Open(filepath.Dir(exePath))
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		m.appendLog("warn", "无法同步面板安装目录: "+err.Error())
	}

	m.appendLog("info", fmt.Sprintf("面板二进制已升级至 %s，准备平滑重启服务", version))

	return func() error {
		err := restart()
		if err != nil {
			m.appendLog("error", "面板重启失败，请手动重启服务: "+err.Error())
		}
		return err
	}, nil
}

func verifyPanelCandidate(path, version string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("升级文件不是 Linux ELF 可执行文件")
	}
	defer f.Close()
	machine, class := elf.EM_NONE, elf.ELFCLASS64
	switch runtime.GOARCH {
	case "arm64":
		machine = elf.EM_AARCH64
	case "amd64":
		machine = elf.EM_X86_64
	case "386":
		machine, class = elf.EM_386, elf.ELFCLASS32
	case "mipsle":
		machine, class = elf.EM_MIPS, elf.ELFCLASS32
	}
	if machine == elf.EM_NONE || f.Machine != machine || f.Class != class || f.Data != elf.ELFDATA2LSB || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) {
		return fmt.Errorf("升级文件架构不匹配: %s", runtime.GOARCH)
	}
	output, err := runCommand(8*time.Second, path, "-version")
	if err != nil || strings.TrimSpace(string(output)) != "daed-next version "+version {
		return fmt.Errorf("新面板二进制版本自检未通过")
	}
	return nil
}

func (m *Manager) panelRestart(exe string) (func() error, error) {
	m.mu.Lock()
	ownsCore := m.cmd != nil
	m.mu.Unlock()
	if ownsCore {
		return nil, fmt.Errorf("dae 由面板直接启动，请先将 dae 交由独立服务管理")
	}

	if exe == "/usr/bin/daed-next" {
		raw, err := runCommand(3*time.Second, "/bin/ubus", "call", "service", "list", `{"name":"daed-next"}`)
		var services map[string]struct {
			Instances map[string]struct {
				PID int `json:"pid"`
			} `json:"instances"`
		}
		if err == nil && json.Unmarshal(raw, &services) == nil {
			for _, instance := range services["daed-next"].Instances {
				if instance.PID == os.Getpid() {
					return func() error {
						cmd := exec.Command(panelInitScript, "restart")
						cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
						if err := cmd.Start(); err != nil {
							return err
						}
						go cmd.Wait()
						return nil
					}, nil
				}
			}
		}
	}
	if sysPath, err := exec.LookPath("systemctl"); err == nil {
		pid, err := runCommand(3*time.Second, sysPath, "show", "--property=MainPID", "--value", "daed-next.service")
		if err == nil && strings.TrimSpace(string(pid)) == strconv.Itoa(os.Getpid()) {
			return func() error {
				_, err := runCommand(5*time.Second, sysPath, "--no-block", "restart", "daed-next.service")
				return err
			}, nil
		}
	}
	return nil, fmt.Errorf("找不到管理当前面板进程的 procd 或 systemd 服务")
}
