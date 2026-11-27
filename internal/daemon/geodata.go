package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"daed-next/internal/model"
)

const (
	geoMaxSize = 96 << 20 // 96 MiB max per .dat file
	geoMinSize = 4 << 10  // 4 KiB min sanity threshold
)

var (
	geoCgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")
	hhmmPattern    = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)
)

// GeoFileStatus describes an on-disk GeoIP or GeoSite database file.
type GeoFileStatus struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Exists    bool   `json:"exists"`
	SizeBytes int64  `json:"sizeBytes"`
	Modified  string `json:"modified,omitempty"`
	HasBackup bool   `json:"hasBackup"`
}

// GeoDataStatus aggregates local file metadata, schedule info, and active update progress.
type GeoDataStatus struct {
	Dir       string             `json:"dir"`
	Updating  bool               `json:"updating"`
	GeoIP     GeoFileStatus      `json:"geoip"`
	GeoSite   GeoFileStatus      `json:"geosite"`
	Config    model.GeoDataConfig `json:"config"`
	NextRunAt string             `json:"nextRunAt,omitempty"`
}

// GeoSourceURLs holds resolved download and optional SHA-256 checksum URLs.
type GeoSourceURLs struct {
	GeoIPURL      string
	GeoIPSumURL   string
	GeoSiteURL    string
	GeoSiteSumURL string
}

// NormalizeGeoDataConfig validates and fills defaults on a GeoDataConfig.
func NormalizeGeoDataConfig(cfg model.GeoDataConfig) (model.GeoDataConfig, error) {
	out := cfg
	out.Preset = strings.ToLower(strings.TrimSpace(out.Preset))
	if out.Preset == "" {
		out.Preset = "loyalsoldier"
	}
	switch out.Preset {
	case "loyalsoldier", "metacubex", "v2fly":
		out.GeoIPURL = ""
		out.GeoSiteURL = ""
		out.GeoIPSumURL = ""
		out.GeoSiteSumURL = ""
	case "custom":
		out.GeoIPURL = strings.TrimSpace(out.GeoIPURL)
		out.GeoSiteURL = strings.TrimSpace(out.GeoSiteURL)
		out.GeoIPSumURL = strings.TrimSpace(out.GeoIPSumURL)
		out.GeoSiteSumURL = strings.TrimSpace(out.GeoSiteSumURL)
		if err := validateSafeHTTPSURL(out.GeoIPURL); err != nil {
			return out, fmt.Errorf("自定义 geoip.dat 地址无效: %w", err)
		}
		if err := validateSafeHTTPSURL(out.GeoSiteURL); err != nil {
			return out, fmt.Errorf("自定义 geosite.dat 地址无效: %w", err)
		}
		if out.GeoIPSumURL != "" {
			if err := validateSafeHTTPSURL(out.GeoIPSumURL); err != nil {
				return out, fmt.Errorf("自定义 geoip 校验地址无效: %w", err)
			}
		}
		if out.GeoSiteSumURL != "" {
			if err := validateSafeHTTPSURL(out.GeoSiteSumURL); err != nil {
				return out, fmt.Errorf("自定义 geosite 校验地址无效: %w", err)
			}
		}
	default:
		return out, fmt.Errorf("不支持的规则库预设类型: %s", out.Preset)
	}

	out.ScheduleMode = strings.ToLower(strings.TrimSpace(out.ScheduleMode))
	if out.ScheduleMode == "" {
		out.ScheduleMode = "daily"
	}
	if out.ScheduleMode != "daily" && out.ScheduleMode != "weekly" && out.ScheduleMode != "interval" {
		return out, fmt.Errorf("不支持的定时周期类型: %s", out.ScheduleMode)
	}

	out.ScheduleTime = strings.TrimSpace(out.ScheduleTime)
	if out.ScheduleTime == "" {
		out.ScheduleTime = "04:00"
	}
	if !hhmmPattern.MatchString(out.ScheduleTime) {
		return out, fmt.Errorf("定时时间格式无效，请使用 24 小时制 HH:MM（如 04:00）")
	}

	if out.ScheduleWeekday < 0 || out.ScheduleWeekday > 6 {
		out.ScheduleWeekday = 1
	}
	if out.IntervalDays <= 0 {
		out.IntervalDays = 3
	} else if out.IntervalDays > 30 {
		out.IntervalDays = 30
	}
	return out, nil
}

// ResolveGeoSources returns download & checksum URLs for a normalized GeoDataConfig.
func ResolveGeoSources(cfg model.GeoDataConfig) GeoSourceURLs {
	switch strings.ToLower(strings.TrimSpace(cfg.Preset)) {
	case "metacubex":
		const base = "https://github.com/MetaCubeX/meta-rules-dat/releases/latest/download/"
		return GeoSourceURLs{
			GeoIPURL:      base + "geoip.dat",
			GeoIPSumURL:   base + "geoip.dat.sha256sum",
			GeoSiteURL:    base + "geosite.dat",
			GeoSiteSumURL: base + "geosite.dat.sha256sum",
		}
	case "v2fly":
		return GeoSourceURLs{
			GeoIPURL:      "https://github.com/v2fly/geoip/releases/latest/download/geoip.dat",
			GeoIPSumURL:   "https://github.com/v2fly/geoip/releases/latest/download/geoip.dat.sha256sum",
			GeoSiteURL:    "https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat",
			GeoSiteSumURL: "https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat.sha256sum",
		}
	case "custom":
		return GeoSourceURLs{
			GeoIPURL:      strings.TrimSpace(cfg.GeoIPURL),
			GeoIPSumURL:   strings.TrimSpace(cfg.GeoIPSumURL),
			GeoSiteURL:    strings.TrimSpace(cfg.GeoSiteURL),
			GeoSiteSumURL: strings.TrimSpace(cfg.GeoSiteSumURL),
		}
	default:
		const base = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/"
		return GeoSourceURLs{
			GeoIPURL:      base + "geoip.dat",
			GeoIPSumURL:   base + "geoip.dat.sha256sum",
			GeoSiteURL:    base + "geosite.dat",
			GeoSiteSumURL: base + "geosite.dat.sha256sum",
		}
	}
}

func parseHHMM(s string) (int, int) {
	m := hhmmPattern.FindStringSubmatch(strings.TrimSpace(s))
	if len(m) != 3 {
		return 4, 0
	}
	h, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	return h, min
}

// NextGeoUpdateRun computes the next scheduled execution time when AutoUpdate is enabled.
func NextGeoUpdateRun(cfg model.GeoDataConfig, now time.Time) time.Time {
	norm, err := NormalizeGeoDataConfig(cfg)
	if err != nil || !norm.AutoUpdate {
		return time.Time{}
	}
	loc := now.Location()
	h, min := parseHHMM(norm.ScheduleTime)
	todaySlot := time.Date(now.Year(), now.Month(), now.Day(), h, min, 0, 0, loc)

	switch norm.ScheduleMode {
	case "weekly":
		diff := (norm.ScheduleWeekday - int(now.Weekday()) + 7) % 7
		slot := todaySlot.AddDate(0, 0, diff)
		if !now.Before(slot) {
			slot = slot.AddDate(0, 0, 7)
		}
		return slot
	case "interval":
		if last, err := time.Parse(time.RFC3339, norm.LastUpdatedAt); err == nil {
			localLast := last.In(loc)
			baseSlot := time.Date(localLast.Year(), localLast.Month(), localLast.Day(), h, min, 0, 0, loc)
			targetSlot := baseSlot.AddDate(0, 0, norm.IntervalDays)
			if now.Before(targetSlot) {
				return targetSlot
			}
		}
		if now.Before(todaySlot) {
			return todaySlot
		}
		return todaySlot.AddDate(0, 0, 1)
	default: // "daily"
		if now.Before(todaySlot) {
			return todaySlot
		}
		return todaySlot.AddDate(0, 0, 1)
	}
}

// IsGeoUpdateDue reports whether a scheduled GeoData update should fire at `now`.
// Updates fire within a 90-minute maintenance window starting at ScheduleTime so router reboots
// during daytime/evening never trigger unexpected background downloads outside the chosen window.
func IsGeoUpdateDue(cfg model.GeoDataConfig, lastSlotKey string, now time.Time) (bool, string) {
	norm, err := NormalizeGeoDataConfig(cfg)
	if err != nil || !norm.AutoUpdate {
		return false, ""
	}
	loc := now.Location()
	h, min := parseHHMM(norm.ScheduleTime)
	todaySlot := time.Date(now.Year(), now.Month(), now.Day(), h, min, 0, 0, loc)
	slotKey := todaySlot.Format("2006-01-02T15:04")

	if now.Before(todaySlot) || now.Sub(todaySlot) >= 90*time.Minute || lastSlotKey == slotKey {
		return false, slotKey
	}

	var lastUpdated time.Time
	if norm.LastUpdatedAt != "" {
		lastUpdated, _ = time.Parse(time.RFC3339, norm.LastUpdatedAt)
	}
	if !lastUpdated.IsZero() && !lastUpdated.Before(todaySlot) {
		return false, slotKey
	}

	switch norm.ScheduleMode {
	case "weekly":
		if int(now.Weekday()) != norm.ScheduleWeekday {
			return false, slotKey
		}
		return true, slotKey
	case "interval":
		if lastUpdated.IsZero() {
			return true, slotKey
		}
		localLast := lastUpdated.In(loc)
		baseSlot := time.Date(localLast.Year(), localLast.Month(), localLast.Day(), h, min, 0, 0, loc)
		targetSlot := baseSlot.AddDate(0, 0, norm.IntervalDays)
		return !todaySlot.Before(targetSlot), slotKey
	default: // "daily"
		return true, slotKey
	}
}

func isDisallowedGeoAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return !addr.IsValid() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() ||
		geoCgnatPrefix.Contains(addr)
}

func validateSafeHTTPSURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("必须使用有效的 HTTPS 地址")
	}
	host := u.Hostname()
	if idx := strings.IndexByte(host, '%'); idx >= 0 {
		host = host[:idx]
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("禁止使用本机或内网地址")
	}
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil && isDisallowedGeoAddr(ip) {
		return fmt.Errorf("禁止使用本机或内网私有地址")
	}
	return nil
}

func newSafeGeoClient(timeout time.Duration) (*http.Client, *http.Transport) {
	dialer := &net.Dialer{Timeout: 12 * time.Second}
	transport := &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			portNum, err := strconv.Atoi(portStr)
			if err != nil || portNum <= 0 || portNum > 65535 {
				return nil, fmt.Errorf("无效端口")
			}
			cleanHost := host
			if idx := strings.IndexByte(cleanHost, '%'); idx >= 0 {
				cleanHost = cleanHost[:idx]
			}
			var addrs []netip.Addr
			if literal, parseErr := netip.ParseAddr(cleanHost); parseErr == nil {
				addrs = []netip.Addr{literal}
			} else {
				addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
				if err != nil || len(addrs) == 0 {
					return nil, fmt.Errorf("无法解析域名 %s", host)
				}
			}
			for _, ip := range addrs {
				if isDisallowedGeoAddr(ip) {
					return nil, fmt.Errorf("禁止连接内网或保留地址 (SSRF 防护)")
				}
			}
			var lastErr error
			for _, ip := range addrs {
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), portStr))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			return nil, lastErr
		},
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 6 {
				return fmt.Errorf("重定向次数过多")
			}
			return validateSafeHTTPSURL(req.URL.String())
		},
	}
	return client, transport
}

func parseFirstSHA256Hex(raw string) (string, error) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		cand := strings.ToLower(strings.TrimSpace(fields[0]))
		b, err := hex.DecodeString(cand)
		if err == nil && len(b) == sha256.Size {
			return cand, nil
		}
	}
	return "", fmt.Errorf("未找到有效的 64 位 SHA-256 校验值")
}

func fileSHA256Hex(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// GeoDataDir resolves the active directory holding geoip.dat and geosite.dat.
func (m *Manager) GeoDataDir() string {
	if m.mockMode {
		return filepath.Join(filepath.Dir(m.configPath), "geodata")
	}
	if envDir := strings.TrimSpace(os.Getenv("DAE_LOCATION_ASSET")); envDir != "" && filepath.IsAbs(envDir) {
		return filepath.Clean(envDir)
	}
	candidates := []string{"/usr/share/dae", "/usr/local/share/dae", "/etc/dae"}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "geoip.dat")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, "geosite.dat")); err == nil {
			return dir
		}
	}
	return "/usr/share/dae"
}

func inspectSingleGeoFile(dir, name string) GeoFileStatus {
	p := filepath.Join(dir, name)
	out := GeoFileStatus{Name: name, Path: p}
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		out.Exists = true
		out.SizeBytes = st.Size()
		out.Modified = st.ModTime().Format(time.RFC3339)
	}
	if bst, err := os.Stat(p + ".previous"); err == nil && bst.Mode().IsRegular() {
		out.HasBackup = true
	}
	return out
}

// InspectGeoData returns live status for geoip.dat, geosite.dat, and configured schedule.
func (m *Manager) InspectGeoData(cfg model.GeoDataConfig) GeoDataStatus {
	norm, err := NormalizeGeoDataConfig(cfg)
	if err != nil {
		norm = model.DefaultGeoDataConfig()
		norm.LastUpdatedAt = cfg.LastUpdatedAt
		norm.LastStatus = cfg.LastStatus
		norm.LastMessage = cfg.LastMessage
	}
	dir := m.GeoDataDir()
	if m.mockMode {
		_ = os.MkdirAll(dir, 0o755)
		for _, fn := range []string{"geoip.dat", "geosite.dat"} {
			fp := filepath.Join(dir, fn)
			if _, statErr := os.Stat(fp); os.IsNotExist(statErr) {
				_ = os.WriteFile(fp, []byte(strings.Repeat("MOCK-GEODATA-", 512)), 0o644)
			}
		}
	}

	m.mu.Lock()
	updating := m.geoUpdating
	m.mu.Unlock()

	var nextRunStr string
	if next := NextGeoUpdateRun(norm, time.Now()); !next.IsZero() {
		nextRunStr = next.Format(time.RFC3339)
	}

	return GeoDataStatus{
		Dir:       dir,
		Updating:  updating,
		GeoIP:     inspectSingleGeoFile(dir, "geoip.dat"),
		GeoSite:   inspectSingleGeoFile(dir, "geosite.dat"),
		Config:    norm,
		NextRunAt: nextRunStr,
	}
}

// UpdateGeoData downloads, SHA-256 checks, dae-validates, atomically swaps geoip.dat/geosite.dat,
// and smoothly hot-reloads dae if running. `target` may be "all", "geoip", or "geosite".
func (m *Manager) UpdateGeoData(ctx context.Context, cfg model.GeoDataConfig, target string) (string, error) {
	norm, err := NormalizeGeoDataConfig(cfg)
	if err != nil {
		return "", err
	}
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		target = "all"
	}
	if target != "all" && target != "geoip" && target != "geosite" {
		return "", fmt.Errorf("无效更新目标: %s", target)
	}

	if !m.geoMu.TryLock() {
		return "", fmt.Errorf("分流数据库正在更新中，请稍候")
	}
	defer m.geoMu.Unlock()

	m.mu.Lock()
	m.geoUpdating = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.geoUpdating = false
		m.mu.Unlock()
	}()

	dir := m.GeoDataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建规则库目录失败: %w", err)
	}

	if m.mockMode {
		nowPayload := fmt.Sprintf("MOCK-GEODATA-%d-%s", time.Now().UnixNano(), strings.Repeat("X", 4200))
		if target == "all" || target == "geoip" {
			if err := WritePublicFile(filepath.Join(dir, "geoip.dat"), []byte(nowPayload)); err != nil {
				return "", err
			}
		}
		if target == "all" || target == "geosite" {
			if err := WritePublicFile(filepath.Join(dir, "geosite.dat"), []byte(nowPayload)); err != nil {
				return "", err
			}
		}
		msg := "已完成分流数据库更新 (预览模式)"
		m.appendLog("info", msg)
		return msg, nil
	}

	sources := ResolveGeoSources(norm)
	type fileJob struct {
		name   string
		dlURL  string
		sumURL string
	}
	var jobs []fileJob
	if target == "all" || target == "geoip" {
		jobs = append(jobs, fileJob{name: "geoip.dat", dlURL: sources.GeoIPURL, sumURL: sources.GeoIPSumURL})
	}
	if target == "all" || target == "geosite" {
		jobs = append(jobs, fileJob{name: "geosite.dat", dlURL: sources.GeoSiteURL, sumURL: sources.GeoSiteSumURL})
	}

	stageDir, err := os.MkdirTemp(dir, ".daestudio-geodata-*")
	if err != nil {
		return "", fmt.Errorf("创建临时校验目录失败: %w", err)
	}
	defer os.RemoveAll(stageDir)

	client, transport := newSafeGeoClient(180 * time.Second)
	defer transport.CloseIdleConnections()

	var updatedNames []string
	var unchangedNames []string

	for _, job := range jobs {
		destPath := filepath.Join(dir, job.name)
		var expectedHex string

		if job.sumURL != "" {
			sumCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			req, reqErr := http.NewRequestWithContext(sumCtx, http.MethodGet, job.sumURL, nil)
			if reqErr != nil {
				cancel()
				return "", reqErr
			}
			req.Header.Set("User-Agent", "DaeStudio/"+PanelVersion)
			resp, doErr := client.Do(req)
			if doErr != nil {
				cancel()
				return "", fmt.Errorf("获取 %s 校验文件失败: %w", job.name, doErr)
			}
			rawSum, readErr := io.ReadAll(io.LimitReader(resp.Body, 8192))
			resp.Body.Close()
			cancel()
			if resp.StatusCode != http.StatusOK || readErr != nil {
				return "", fmt.Errorf("读取 %s 校验文件返回 HTTP %d", job.name, resp.StatusCode)
			}
			expectedHex, err = parseFirstSHA256Hex(string(rawSum))
			if err != nil {
				return "", fmt.Errorf("解析 %s 校验值失败: %w", job.name, err)
			}

			// Smart skip: if local file already matches upstream SHA-256, avoid 24MB flash rewrite!
			if localHex, localSize, hashErr := fileSHA256Hex(destPath); hashErr == nil && localSize >= geoMinSize && localHex == expectedHex {
				unchangedNames = append(unchangedNames, job.name)
				continue
			}
		}

		dlReq, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, job.dlURL, nil)
		if reqErr != nil {
			return "", reqErr
		}
		dlReq.Header.Set("User-Agent", "DaeStudio/"+PanelVersion)
		dlResp, doErr := client.Do(dlReq)
		if doErr != nil {
			return "", fmt.Errorf("下载 %s 失败: %w", job.name, doErr)
		}
		if dlResp.StatusCode != http.StatusOK {
			dlResp.Body.Close()
			return "", fmt.Errorf("下载 %s 返回 HTTP %d", job.name, dlResp.StatusCode)
		}

		stagedPath := filepath.Join(stageDir, job.name)
		hasher := sha256.New()
		writeErr := writeSyncedFile(stagedPath, io.TeeReader(dlResp.Body, hasher), 0o644, geoMaxSize)
		dlResp.Body.Close()
		if writeErr != nil {
			return "", fmt.Errorf("保存 %s 失败: %w", job.name, writeErr)
		}
		stInfo, statErr := os.Stat(stagedPath)
		if statErr != nil || stInfo.Size() < geoMinSize {
			return "", fmt.Errorf("下载的 %s 文件不完整 (%d B)", job.name, stInfo.Size())
		}

		actualHex := hex.EncodeToString(hasher.Sum(nil))
		if expectedHex != "" && actualHex != expectedHex {
			return "", fmt.Errorf("%s SHA-256 校验未通过（期望 %s…，实际 %s…）", job.name, expectedHex[:12], actualHex[:12])
		}
		updatedNames = append(updatedNames, job.name)
	}

	if len(updatedNames) == 0 {
		msg := fmt.Sprintf("分流数据库已是最新版本（%s SHA-256 校验一致，无需重复写入）", strings.Join(unchangedNames, "、"))
		m.appendLog("info", msg)
		return msg, nil
	}

	// Link/copy untouched sibling .dat into stageDir so `DAE_LOCATION_ASSET=stageDir dae validate` sees both files.
	for _, sibling := range []string{"geoip.dat", "geosite.dat"} {
		stagedSibling := filepath.Join(stageDir, sibling)
		if _, err := os.Stat(stagedSibling); err == nil {
			continue
		}
		existingSibling := filepath.Join(dir, sibling)
		if _, err := os.Stat(existingSibling); err == nil {
			if os.Link(existingSibling, stagedSibling) != nil {
				_ = os.Symlink(existingSibling, stagedSibling)
			}
		}
	}

	// Preflight compatibility check against current config.dae before touching live files!
	if binPath, coreFound := m.resolveDaeBin(); coreFound && runtime.GOOS == "linux" {
		if _, cfgErr := os.Stat(m.configPath); cfgErr == nil {
			_, hasIP := os.Stat(filepath.Join(stageDir, "geoip.dat"))
			_, hasSite := os.Stat(filepath.Join(stageDir, "geosite.dat"))
			if hasIP == nil && hasSite == nil {
				valCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
				cmd := exec.CommandContext(valCtx, binPath, "validate", "-c", m.configPath)
				cmd.Env = append(os.Environ(), "DAE_LOCATION_ASSET="+stageDir)
				cmd.WaitDelay = 2 * time.Second
				out, valErr := cmd.CombinedOutput()
				cancel()
				if valErr != nil {
					detail := strings.TrimSpace(string(out))
					return "", fmt.Errorf("新版规则库与当前配置不兼容，已中止替换: %v %s", valErr, detail)
				}
			}
		}
	}

	type swappedPair struct {
		dest   string
		backup string
		hadOld bool
	}
	var swapped []swappedPair

	for _, name := range updatedNames {
		stagedPath := filepath.Join(stageDir, name)
		destPath := filepath.Join(dir, name)
		backupPath := destPath + ".previous"
		hadOld := false

		if oldFile, openErr := os.Open(destPath); openErr == nil {
			bakStage := filepath.Join(stageDir, name+".bak")
			bakErr := writeSyncedFile(bakStage, oldFile, 0o644, geoMaxSize)
			oldFile.Close()
			if bakErr != nil {
				return "", fmt.Errorf("备份原 %s 失败: %w", name, bakErr)
			}
			if renErr := os.Rename(bakStage, backupPath); renErr != nil {
				return "", fmt.Errorf("写入备份 %s 失败: %w", backupPath, renErr)
			}
			hadOld = true
		}

		if err := os.Rename(stagedPath, destPath); err != nil {
			for _, prev := range swapped {
				if prev.hadOld {
					_ = os.Rename(prev.backup, prev.dest)
				}
			}
			return "", fmt.Errorf("替换 %s 失败: %w", name, err)
		}
		swapped = append(swapped, swappedPair{dest: destPath, backup: backupPath, hadOld: hadOld})
	}

	if d, openErr := os.Open(dir); openErr == nil {
		_ = d.Sync()
		d.Close()
	}

	reloaded := false
	if pid := detectDaePID(); pid > 0 {
		if reloadErr := m.reloadCurrentConfig(pid, false); reloadErr != nil {
			for _, prev := range swapped {
				if prev.hadOld {
					_ = os.Rename(prev.backup, prev.dest)
				}
			}
			if pid2 := detectDaePID(); pid2 > 0 {
				_ = m.reloadCurrentConfig(pid2, false)
			}
			return "", fmt.Errorf("更新后重载 dae 失败，已自动回滚旧版规则库: %w", reloadErr)
		}
		reloaded = true
	}

	summary := fmt.Sprintf("已更新 %s", strings.Join(updatedNames, "、"))
	if len(unchangedNames) > 0 {
		summary += fmt.Sprintf("（%s 已是最新）", strings.Join(unchangedNames, "、"))
	}
	if reloaded {
		summary += "，并已平滑热重载 dae 生效"
	}
	m.appendLog("info", summary)
	return summary, nil
}

// WritePublicFile atomically writes a world-readable (0644) data file with fsync.
func WritePublicFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".daestudio-pub-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
