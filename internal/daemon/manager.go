package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"daed-next/internal/compiler"
)

// LogEntry represents a structured daemon log line.
type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// RuntimeStats holds live service status and traffic telemetry.
type RuntimeStats struct {
	Running         bool                       `json:"running"`
	MockMode        bool                       `json:"mockMode"`
	PID             int                        `json:"pid"`
	UptimeSeconds   int64                      `json:"uptimeSeconds"`
	UploadBps       int64                      `json:"uploadBps"`
	DownloadBps     int64                      `json:"downloadBps"`
	TotalUpload     int64                      `json:"totalUpload"`
	TotalDownload   int64                      `json:"totalDownload"`
	MemoryMB        float64                    `json:"memoryMb"`
	ActiveConns     int                        `json:"activeConns"`
	ConfigPath      string                     `json:"configPath"`
	LastReloadedAt  string                     `json:"lastReloadedAt"`
	EndpointTraffic map[string]EndpointTraffic `json:"endpointTraffic,omitempty"`
}

// Manager supervises the dae kernel process (integrating natively with OpenWrt procd /etc/init.d/dae when present).
type Manager struct {
	mu             sync.Mutex
	daeBin         string
	configPath     string
	hasInitScript  bool
	mockMode       bool
	running        bool
	pid            int
	startedAt      time.Time
	lastReloadedAt string
	cmd            *exec.Cmd
	logs           []LogEntry
	maxLogs        int
	logMinOffset   int64

	lastNetSample time.Time
	lastRxBytes   int64
	lastTxBytes   int64
	lastUpBps     int64
	lastDownBps   int64
	totalRx       int64
	totalTx       int64
	tickSeq       int64
	traffic       *nodeTrafficTracker
	geoMu         sync.Mutex
	geoUpdating   bool
}

func NewManager(daeBin, configPath string, forceMock bool) *Manager {
	isMock := forceMock
	if path, err := exec.LookPath(daeBin); err == nil {
		daeBin = path
	}

	_, initErr := os.Stat("/etc/init.d/dae")
	m := &Manager{
		daeBin:        daeBin,
		configPath:    configPath,
		hasInitScript: runtime.GOOS == "linux" && initErr == nil,
		mockMode:      isMock,
		maxLogs:       400,
		traffic:       newNodeTrafficTracker(),
	}
	if isMock {
		m.running = true
		m.pid = os.Getpid()
		m.startedAt = time.Now()
		m.lastReloadedAt = time.Now().Format(time.RFC3339)
		m.appendLog("info", "DaeStudio 已在模拟内核模式 (Mock Engine) 启动")
	} else {
		if pid := detectDaePID(); pid > 0 {
			m.running = true
			m.pid = pid
			m.startedAt = detectProcStartTime(pid)
			m.appendLog("info", fmt.Sprintf("检测到运行中的 dae 进程 (PID=%d)，已自动接管监控", pid))
		} else {
			m.appendLog("info", fmt.Sprintf("DaeStudio 控制面已就绪 (内核路径: %s, 配置: %s)", daeBin, configPath))
		}
	}
	return m
}

func (m *Manager) ConfigPath() string {
	return m.configPath
}

func (m *Manager) WriteConfigFile(cfg string) error {
	if err := os.MkdirAll(filepath.Dir(m.configPath), 0o755); err != nil {
		return err
	}
	return WritePrivateFile(m.configPath, []byte(cfg))
}

// WritePrivateFile atomically replaces a configuration or state file while preserving symlinks.
func WritePrivateFile(path string, data []byte) error {
	targetPath := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != "" {
		targetPath = resolved
	}
	f, err := os.CreateTemp(filepath.Dir(targetPath), ".daestudio-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
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
	return os.Rename(f.Name(), targetPath)
}

func runCommand(limit time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("执行 %s 超时（%s）", filepath.Base(name), limit)
	}
	return out, err
}

func (m *Manager) Validate(cfg string) (bool, string) {
	errs := compiler.ValidateSyntax(cfg)
	if len(errs) > 0 {
		msg := strings.Join(errs, "\n")
		m.appendLog("error", "配置静态结构校验失败: "+msg)
		return false, msg
	}
	if m.mockMode {
		m.appendLog("info", "配置语法校验通过 (Static & DSL Validator OK)")
		return true, "配置语法与路由结构校验通过"
	}

	dir := filepath.Dir(m.configPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err.Error()
	}
	f, err := os.CreateTemp(dir, ".daestudio-validate-*.dae")
	if err != nil {
		return false, err.Error()
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(cfg); err != nil {
		f.Close()
		return false, err.Error()
	}
	if err := f.Close(); err != nil {
		return false, err.Error()
	}

	out, err := runCommand(45*time.Second, m.daeBin, "validate", "-c", f.Name())
	outputStr := strings.TrimSpace(string(out))
	if err != nil {
		msg := err.Error()
		if outputStr != "" {
			msg += ": " + outputStr
		}
		m.appendLog("error", "dae validate 校验未通过: "+msg)
		return false, msg
	}
	m.appendLog("info", "dae validate 内核级配置语法校验通过")
	if outputStr == "" {
		outputStr = "dae validate 内核级配置语法校验通过"
	}
	return true, outputStr
}

// ApplyConfig validates before publishing the candidate and restores the prior
// on-disk file if the core rejects the operation. A single writer calls it.
func (m *Manager) ApplyConfig(cfg string, apply, abortConnections bool) error {
	ok, msg := m.Validate(cfg)
	if !ok {
		return fmt.Errorf("配置校验未通过: %s", msg)
	}
	previous, err := os.ReadFile(m.configPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	hadPrevious := err == nil
	if err := m.WriteConfigFile(cfg); err != nil {
		return err
	}
	if !apply {
		return nil
	}

	if m.mockMode {
		return m.startCurrentConfig()
	}
	if pid := detectDaePID(); pid > 0 {
		err = m.reloadCurrentConfig(pid, abortConnections)
	} else {
		err = m.startCurrentConfig()
	}
	if err == nil {
		return nil
	}

	var rollbackErr error
	if hadPrevious {
		rollbackErr = m.WriteConfigFile(string(previous))
	} else {
		rollbackErr = os.Remove(m.configPath)
	}
	if rollbackErr != nil {
		return errors.Join(err, fmt.Errorf("恢复上一份配置失败: %w", rollbackErr))
	}
	m.appendLog("warn", "应用失败，已恢复上一份配置文件")
	return fmt.Errorf("%w（已恢复上一份配置文件）", err)
}

func (m *Manager) Start(cfg string) error {
	return m.ApplyConfig(cfg, true, false)
}

func (m *Manager) startCurrentConfig() error {
	if m.mockMode {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.running = true
		m.pid = os.Getpid()
		if m.startedAt.IsZero() {
			m.startedAt = time.Now()
		}
		m.lastReloadedAt = time.Now().Format(time.RFC3339)
		m.appendLogUnlocked("info", "配置已应用 (Mock Engine)")
		return nil
	}
	if m.hasInitScript {
		for _, args := range [][]string{{"set", "dae.config.enabled=1"}, {"set", "dae.config.config_file=" + m.configPath}, {"commit", "dae"}} {
			out, err := runCommand(5*time.Second, "uci", args...)
			if err != nil {
				return fmt.Errorf("更新 dae 服务设置失败: %w %s", err, strings.TrimSpace(string(out)))
			}
		}
		out, err := runCommand(50*time.Second, "/etc/init.d/dae", "start")
		if err != nil {
			return fmt.Errorf("启动 dae 失败: %w %s", err, strings.TrimSpace(string(out)))
		}
		for i := 0; i < 20; i++ {
			if pid := detectDaePID(); pid > 0 {
				m.recordRunning(pid)
				return nil
			}
			time.Sleep(250 * time.Millisecond)
		}
		return fmt.Errorf("启动命令结束后未找到 dae 进程，请查看运行日志")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Keep core logging independent of the panel's lifetime. Pipes lose their
	// reader when the panel restarts, which can break the core during reload.
	logDir := "/var/log/dae"
	if runtime.GOOS != "linux" {
		logDir = filepath.Dir(m.configPath)
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	cmd := exec.Command(m.daeBin, "run", "-c", m.configPath,
		"--logfile", filepath.Join(logDir, "dae.log"),
		"--logfile-maxsize", "2", "--logfile-maxbackups", "1")
	if err := cmd.Start(); err != nil {
		m.appendLogUnlocked("error", "启动 dae 失败: "+err.Error())
		return err
	}

	m.cmd = cmd
	m.running = true
	m.pid = cmd.Process.Pid
	m.startedAt = time.Now()
	m.lastReloadedAt = time.Now().Format(time.RFC3339)
	m.appendLogUnlocked("info", fmt.Sprintf("dae 核心进程已启动 (PID=%d)", m.pid))

	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.cmd == cmd {
			m.running = false
			m.pid = 0
			m.cmd = nil
			if err != nil {
				m.appendLogUnlocked("warn", "dae 进程退出: "+err.Error())
			} else {
				m.appendLogUnlocked("info", "dae 进程已正常停止")
			}
		}
	}()
	return nil
}

func (m *Manager) Stop() error {
	m.mu.Lock()
	if m.mockMode {
		m.running = false
		m.pid = 0
		m.appendLogUnlocked("info", "dae 服务已停止 (Mock Engine)")
		m.mu.Unlock()
		return nil
	}
	hasInit := m.hasInitScript
	var childProc *os.Process
	if m.cmd != nil {
		childProc = m.cmd.Process
	}
	m.mu.Unlock()

	if hasInit {
		_, _ = runCommand(5*time.Second, "uci", "set", "dae.config.enabled=0")
		_, _ = runCommand(5*time.Second, "uci", "commit", "dae")
		if out, err := runCommand(15*time.Second, "/etc/init.d/dae", "stop"); err != nil {
			m.appendLog("warn", fmt.Sprintf("执行 init.d stop 返回异常: %v %s", err, strings.TrimSpace(string(out))))
		}
	}
	if childProc != nil {
		_ = childProc.Signal(syscall.SIGTERM)
	} else if pid := detectDaePID(); pid > 0 {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
	}
	stillRunning := true
	for i := 0; i < 12; i++ {
		if detectDaePID() == 0 {
			stillRunning = false
			break
		}
		time.Sleep(150 * time.Millisecond)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = stillRunning
	if !stillRunning {
		m.pid = 0
		m.appendLogUnlocked("info", "dae 核心服务已停止")
		return nil
	}
	m.appendLogUnlocked("error", "停止命令已执行，但检测到 dae 进程仍在运行")
	return fmt.Errorf("停止超时，dae 进程仍在运行")
}

func (m *Manager) Reload(cfg string) error {
	return m.ApplyConfig(cfg, true, false)
}

func (m *Manager) recordRunning(pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running || m.pid != pid {
		m.startedAt = detectProcStartTime(pid)
	}
	m.running = true
	m.pid = pid
	m.lastReloadedAt = time.Now().Format(time.RFC3339)
}

func (m *Manager) reloadCurrentConfig(pid int, abortConnections bool) error {
	args := []string{"reload", strconv.Itoa(pid)}
	if abortConnections {
		args = append(args, "--abort")
	}
	// dae reports its own reload deadline after about 60 seconds. Do not send
	// a second signal when the first command reports failure or is still busy.
	out, err := runCommand(68*time.Second, m.daeBin, args...)
	if err != nil {
		detail := strings.TrimSpace(string(out))
		m.appendLog("error", fmt.Sprintf("dae 重载失败: %v %s", err, detail))
		return fmt.Errorf("dae 重载失败: %w %s", err, detail)
	}
	if !isDaeRunPID(pid) {
		return fmt.Errorf("dae 在重载后退出，请查看运行日志")
	}
	m.recordRunning(pid)
	m.appendLog("info", fmt.Sprintf("dae 已确认重载完成 (PID=%d)", pid))
	return nil
}

func (m *Manager) SyncNodeEndpoints(targets []EndpointTarget) {
	if m.traffic != nil {
		m.traffic.SyncTargets(targets)
	}
}

func (m *Manager) Stats() RuntimeStats {
	m.mu.Lock()
	if !m.mockMode {
		pid := detectDaePID()
		if pid > 0 {
			if !m.running || m.pid != pid {
				m.running = true
				m.pid = pid
				m.startedAt = detectProcStartTime(pid)
			}
		} else {
			m.running = false
			m.pid = 0
		}
	}

	var uptime int64
	if m.running && !m.startedAt.IsZero() {
		uptime = int64(time.Since(m.startedAt).Seconds())
	}

	upBps, downBps := m.sampleBandwidthUnlocked()
	running := m.running
	mockMode := m.mockMode
	pid := m.pid
	totalTx := m.totalTx
	totalRx := m.totalRx
	cfgPath := m.configPath
	lastReloaded := m.lastReloadedAt
	tickSeq := uint64(m.tickSeq)
	m.mu.Unlock()

	memMB := detectProcMemoryMB(pid)
	conns := detectActiveConns(running, mockMode, int64(tickSeq))
	var epTraffic map[string]EndpointTraffic
	if m.traffic != nil {
		epTraffic = m.traffic.Sample(running, mockMode, tickSeq)
	}

	return RuntimeStats{
		Running:         running,
		MockMode:        mockMode,
		PID:             pid,
		UptimeSeconds:   uptime,
		UploadBps:       upBps,
		DownloadBps:     downBps,
		TotalUpload:     totalTx,
		TotalDownload:   totalRx,
		MemoryMB:        memMB,
		ActiveConns:     conns,
		ConfigPath:      cfgPath,
		LastReloadedAt:  lastReloaded,
		EndpointTraffic: epTraffic,
	}
}

func (m *Manager) Logs() []LogEntry {
	m.mu.Lock()
	internalLogs := make([]LogEntry, len(m.logs))
	copy(internalLogs, m.logs)
	minOff := m.logMinOffset
	m.mu.Unlock()

	fileLogs := readDaeLogFile("/var/log/dae/dae.log", 120, minOff)
	if len(fileLogs) == 0 {
		return internalLogs
	}
	combined := append(fileLogs, internalLogs...)
	if len(combined) > 300 {
		combined = combined[len(combined)-300:]
	}
	return combined
}

func (m *Manager) ClearLogs() {
	var nextOffset int64
	if runtime.GOOS == "linux" {
		if st, err := os.Stat("/var/log/dae/dae.log"); err == nil {
			nextOffset = st.Size()
		}
	}
	m.mu.Lock()
	m.logs = nil
	m.logMinOffset = nextOffset
	m.appendLogUnlocked("info", "面板日志缓冲区已清空")
	m.mu.Unlock()
}

func (m *Manager) sampleBandwidthUnlocked() (int64, int64) {
	m.tickSeq++
	if runtime.GOOS == "linux" {
		rx, tx, ok := readLinuxNetDev()
		now := time.Now()
		if ok {
			if !m.lastNetSample.IsZero() {
				dt := now.Sub(m.lastNetSample).Seconds()
				if dt < 0.8 {
					return m.lastUpBps, m.lastDownBps
				}
				downBps := int64(float64(rx-m.lastRxBytes) / dt)
				upBps := int64(float64(tx-m.lastTxBytes) / dt)
				if downBps < 0 {
					downBps = 0
				}
				if upBps < 0 {
					upBps = 0
				}
				m.lastRxBytes = rx
				m.lastTxBytes = tx
				m.lastUpBps = upBps
				m.lastDownBps = downBps
				m.lastNetSample = now
				m.totalRx += int64(float64(downBps) * dt)
				m.totalTx += int64(float64(upBps) * dt)
				return upBps, downBps
			}
			m.lastRxBytes = rx
			m.lastTxBytes = tx
			m.lastNetSample = now
			return 0, 0
		}
	}
	if !m.running {
		return 0, 0
	}
	t := float64(m.tickSeq)
	down := int64(820_000 + 640_000*math.Sin(t*0.45) + 290_000*math.Cos(t*1.1))
	up := int64(190_000 + 110_000*math.Cos(t*0.55) + 65_000*math.Sin(t*0.9))
	if down < 32_000 {
		down = 32_000
	}
	if up < 12_000 {
		up = 12_000
	}
	m.totalRx += down
	m.totalTx += up
	return up, down
}

func detectDaePID() int {
	if runtime.GOOS != "linux" {
		return 0
	}
	if b, err := os.ReadFile("/var/run/dae.pid"); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
			if isDaeRunPID(pid) {
				return pid
			}
		}
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	selfPID := os.Getpid()
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(ent.Name())
		if err != nil || pid <= 1 || pid == selfPID {
			continue
		}
		if isDaeRunPID(pid) {
			return pid
		}
	}
	return 0
}

func isDaeRunPID(pid int) bool {
	// validate/reload also have comm=dae; only the long-running core accepts a reload.
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	args := strings.Split(string(cmdline), "\x00")
	return err == nil && len(args) > 2 && filepath.Base(args[0]) == "dae" && args[1] == "run"
}

func detectProcStartTime(pid int) time.Time {
	if info, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
		return info.ModTime()
	}
	return time.Now()
}

func detectProcMemoryMB(pid int) float64 {
	if runtime.GOOS == "linux" && pid > 0 {
		if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid)); err == nil {
			fields := strings.Fields(string(b))
			if len(fields) >= 2 {
				rssPages, _ := strconv.ParseFloat(fields[1], 64)
				mb := (rssPages * float64(os.Getpagesize())) / 1024.0 / 1024.0
				return math.Round(mb*10) / 10
			}
		}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return math.Round((float64(ms.Alloc)/1024.0/1024.0)*10) / 10
}

func detectActiveConns(running, mock bool, seq int64) int {
	if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/proc/sys/net/netfilter/nf_conntrack_count"); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return n
			}
		}
	}
	if !running {
		return 0
	}
	if mock {
		return 38 + int(seq%19)
	}
	return 0
}

func readDaeLogFile(path string, maxLines int, minOffset int64) []LogEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	// Read only the tail 64KB of the log file (and never before minOffset after ClearLogs) to avoid sparse NUL holes
	if st, statErr := f.Stat(); statErr == nil {
		start := int64(0)
		if minOffset > 0 && st.Size() >= minOffset {
			start = minOffset
		}
		if st.Size()-start > 65536 {
			start = st.Size() - 65536
		}
		if start > 0 {
			_, _ = f.Seek(start, io.SeekStart)
		}
	}
	b, err := io.ReadAll(f)
	if err != nil || len(b) == 0 {
		return nil
	}
	cleanStr := strings.ReplaceAll(string(b), "\x00", "")
	lines := strings.Split(strings.TrimSpace(cleanStr), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := make([]LogEntry, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		lvl := "info"
		low := strings.ToLower(ln)
		if strings.Contains(low, "level=error") || strings.Contains(low, "fatal") {
			lvl = "error"
		} else if strings.Contains(low, "level=warn") {
			lvl = "warn"
		} else if strings.Contains(low, "level=debug") {
			lvl = "debug"
		}
		out = append(out, LogEntry{
			Time:    "dae",
			Level:   lvl,
			Message: ln,
		})
	}
	return out
}

func readLinuxNetDev() (int64, int64, bool) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	wanIface := detectDefaultRouteIface()
	var rxTotal, txTotal int64
	for _, line := range strings.Split(string(b), "\n") {
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		iface := strings.TrimSpace(line[:idx])
		fields := strings.Fields(line[idx+1:])
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseInt(fields[0], 10, 64)
		tx, _ := strconv.ParseInt(fields[8], 10, 64)
		if wanIface != "" && iface == wanIface {
			return rx, tx, true
		}
		if isVirtualOrBridgeIface(iface) {
			continue
		}
		rxTotal += rx
		txTotal += tx
	}
	return rxTotal, txTotal, true
}

func detectDefaultRouteIface() string {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for i, line := range strings.Split(string(b), "\n") {
		if i == 0 {
			continue
		}
		fields := strings.Fields(line)
		// Destination == 00000000 and RTF_UP (0x1) set
		if len(fields) >= 4 && fields[1] == "00000000" {
			flags, err := strconv.ParseUint(fields[3], 16, 32)
			if err == nil && (flags&0x1) != 0 {
				return fields[0]
			}
		}
	}
	return ""
}

func isVirtualOrBridgeIface(iface string) bool {
	if iface == "lo" {
		return true
	}
	for _, prefix := range []string{
		"docker", "veth", "br-", "dae", "virbr", "tun", "tap",
		"sit", "ip6", "gre", "erspan", "teql", "dummy", "ifb",
	} {
		if strings.HasPrefix(iface, prefix) {
			return true
		}
	}
	return false
}

func (m *Manager) streamLogs(r io.Reader) {
	if r == nil {
		return
	}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		lvl := "info"
		low := strings.ToLower(line)
		if strings.Contains(low, "err") || strings.Contains(low, "fatal") {
			lvl = "error"
		} else if strings.Contains(low, "warn") {
			lvl = "warn"
		} else if strings.Contains(low, "debug") {
			lvl = "debug"
		}
		m.appendLog(lvl, line)
	}
}

func (m *Manager) appendLog(level, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendLogUnlocked(level, msg)
}

func (m *Manager) appendLogUnlocked(level, msg string) {
	entry := LogEntry{
		Time:    time.Now().Format("15:04:05"),
		Level:   level,
		Message: msg,
	}
	m.logs = append(m.logs, entry)
	if len(m.logs) > m.maxLogs {
		m.logs = m.logs[len(m.logs)-m.maxLogs:]
	}
}
