package daemon

import (
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	kernelOnce sync.Once
	kernelVer  string

	daeVerMu    sync.Mutex
	daeVerPath  string
	daeVerMtime time.Time
	daeVerSize  int64
	daeVerStr   string
)

func cachedDaeVersion(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	daeVerMu.Lock()
	if daeVerPath == path && daeVerSize == info.Size() && daeVerMtime.Equal(info.ModTime()) && daeVerStr != "" {
		v := daeVerStr
		daeVerMu.Unlock()
		return v
	}
	daeVerMu.Unlock()

	out, err := runCommand(5*time.Second, path, "--version")
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	daeVerMu.Lock()
	daeVerPath = path
	daeVerSize = info.Size()
	daeVerMtime = info.ModTime()
	daeVerStr = v
	daeVerMu.Unlock()
	return v
}

// CheckItem represents a single diagnostic check in System Doctor.
type CheckItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"` // pass, warn, fail, info
	Current     string `json:"current"`
	Requirement string `json:"requirement"`
	Remediation string `json:"remediation,omitempty"`
}

// NetInterface describes a host network interface for WAN/LAN binding selection.
type NetInterface struct {
	Name  string   `json:"name"`
	IPs   []string `json:"ips"`
	Flags string   `json:"flags"`
}

// DoctorReport aggregates environment health checks and host interfaces.
type DoctorReport struct {
	OS             string         `json:"os"`
	Arch           string         `json:"arch"`
	Kernel         string         `json:"kernel"`
	MockMode       bool           `json:"mockMode"`
	ReadyForDae    bool           `json:"readyForDae"`
	DaeBinaryFound bool           `json:"daeBinaryFound"`
	DaeVersion     string         `json:"daeVersion"`
	Checks         []CheckItem    `json:"checks"`
	Interfaces     []NetInterface `json:"interfaces"`
}

// RunSystemDoctor inspects kernel eBPF/BTF prerequisites and network interfaces.
func RunSystemDoctor(daeBin string, mockMode bool) DoctorReport {
	rep := DoctorReport{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Kernel:     detectKernelVersion(),
		MockMode:   mockMode || runtime.GOOS != "linux",
		Interfaces: listHostInterfaces(),
	}

	trimmedBin := strings.TrimSpace(daeBin)
	if trimmedBin == "" {
		trimmedBin = "dae"
	}
	if path, err := exec.LookPath(trimmedBin); err == nil && path != "" {
		if st, statErr := os.Stat(path); statErr == nil && !st.IsDir() {
			rep.DaeBinaryFound = true
			rep.DaeVersion = cachedDaeVersion(path)
		}
	}
	if !rep.DaeBinaryFound && (trimmedBin == "dae" || trimmedBin == "/usr/bin/dae" || trimmedBin == "/usr/local/bin/dae") {
		for _, cand := range []string{"/usr/bin/dae", "/usr/local/bin/dae"} {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
				rep.DaeBinaryFound = true
				rep.DaeVersion = cachedDaeVersion(cand)
				break
			}
		}
	}

	if runtime.GOOS != "linux" {
		rep.ReadyForDae = true
		rep.DaeVersion = defaultIfEmpty(rep.DaeVersion, "dae v0.9.0 (Mock Preview Engine)")
		rep.Checks = []CheckItem{
			{
				ID:          "os-mode",
				Title:       "运行环境模式",
				Status:      "info",
				Current:     runtime.GOOS + "/" + runtime.GOARCH + " (本地模拟预览模式)",
				Requirement: "生产部署需 Linux >= 5.17 (eBPF TC/TPROXY)",
				Remediation: "当前为非 Linux 环境，已自动启用 Mock 守护引擎，所有配置编译、订阅转换、规则编排与真实延迟测速均可正常使用。",
			},
			{
				ID:          "btf-vmlinux",
				Title:       "内核 BTF 调试符号 (/sys/kernel/btf/vmlinux)",
				Status:      "pass",
				Current:     "Simulated OK (CO-RE BTF Ready)",
				Requirement: "CONFIG_DEBUG_INFO_BTF=y",
			},
			{
				ID:          "bpf-fs",
				Title:       "BPF 文件系统挂载 (/sys/fs/bpf)",
				Status:      "pass",
				Current:     "Simulated Mounted (bpffs)",
				Requirement: "mount -t bpf bpf /sys/fs/bpf",
			},
			{
				ID:          "ip-forward",
				Title:       "IPv4 / IPv6 转发参数",
				Status:      "pass",
				Current:     "auto_config_kernel_parameter: true",
				Requirement: "net.ipv4.ip_forward=1",
			},
		}
		return rep
	}

	// Real Linux eBPF checks
	allPass := true

	// 1. dae binary
	if rep.DaeBinaryFound {
		rep.Checks = append(rep.Checks, CheckItem{
			ID:          "dae-bin",
			Title:       "dae 核心二进制",
			Status:      "pass",
			Current:     defaultIfEmpty(rep.DaeVersion, daeBin),
			Requirement: "已安装可执行文件且具备 CAP_BPF / root 权限",
		})
	} else {
		allPass = false
		rep.Checks = append(rep.Checks, CheckItem{
			ID:          "dae-bin",
			Title:       "dae 核心二进制",
			Status:      "fail",
			Current:     "未找到 " + daeBin,
			Requirement: "需安装 dae 核心至 /usr/bin/dae 或通过 -dae-bin 指定路径",
			Remediation: "可在面板概览页或上方「dae 内核管理」一键下载安装官方内核，或执行 opkg install dae / apk add dae。",
		})
	}

	// 2. BTF support
	if _, err := os.Stat("/sys/kernel/btf/vmlinux"); err == nil {
		rep.Checks = append(rep.Checks, CheckItem{
			ID:          "btf",
			Title:       "内核 BTF 支持 (/sys/kernel/btf/vmlinux)",
			Status:      "pass",
			Current:     "已启用 (CO-RE 就绪)",
			Requirement: "CONFIG_DEBUG_INFO_BTF=y",
		})
	} else {
		allPass = false
		rep.Checks = append(rep.Checks, CheckItem{
			ID:          "btf",
			Title:       "内核 BTF 支持 (/sys/kernel/btf/vmlinux)",
			Status:      "fail",
			Current:     "缺失 /sys/kernel/btf/vmlinux",
			Requirement: "CONFIG_DEBUG_INFO_BTF=y",
			Remediation: "请更换开启了 BTF 支持的 Linux/OpenWrt 内核（推荐 OpenWrt 23.05/24.10+ 或 Linux 5.17+）。",
		})
	}

	// 3. bpffs mount
	mounts, _ := os.ReadFile("/proc/mounts")
	if strings.Contains(string(mounts), "/sys/fs/bpf") {
		rep.Checks = append(rep.Checks, CheckItem{
			ID:          "bpffs",
			Title:       "BPF 文件系统 (/sys/fs/bpf)",
			Status:      "pass",
			Current:     "已挂载",
			Requirement: "bpffs mounted on /sys/fs/bpf",
		})
	} else {
		allPass = false
		rep.Checks = append(rep.Checks, CheckItem{
			ID:          "bpffs",
			Title:       "BPF 文件系统 (/sys/fs/bpf)",
			Status:      "warn",
			Current:     "未挂载",
			Requirement: "bpffs mounted on /sys/fs/bpf",
			Remediation: "执行 `mount -t bpf bpf /sys/fs/bpf` 或在 LXC 容器配置中允许挂载 bpf 文件系统。",
		})
	}

	// 4. IPv4 Forwarding
	ipfwd, _ := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if strings.TrimSpace(string(ipfwd)) == "1" {
		rep.Checks = append(rep.Checks, CheckItem{
			ID:          "ipfwd",
			Title:       "内核 IPv4 流量转发",
			Status:      "pass",
			Current:     "net.ipv4.ip_forward = 1",
			Requirement: "旁路由 / 网关透明代理必需开启",
		})
	} else {
		rep.Checks = append(rep.Checks, CheckItem{
			ID:          "ipfwd",
			Title:       "内核 IPv4 流量转发",
			Status:      "warn",
			Current:     "net.ipv4.ip_forward = 0",
			Requirement: "作为网关或旁路由时需为 1",
			Remediation: "可在面板全局参数开启 `auto_config_kernel_parameter: true`，或执行 `sysctl -w net.ipv4.ip_forward=1`。",
		})
	}

	rep.ReadyForDae = allPass
	return rep
}

func detectKernelVersion() string {
	kernelOnce.Do(func() {
		out, err := runCommand(3*time.Second, "uname", "-r")
		if err != nil {
			kernelVer = "unknown"
			return
		}
		kernelVer = strings.TrimSpace(string(out))
	})
	return kernelVer
}

func listHostInterfaces() []NetInterface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return []NetInterface{{Name: "eth0", IPs: []string{"192.168.1.1/24"}, Flags: "up|broadcast"}}
	}
	var out []NetInterface
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		var ips []string
		for _, a := range addrs {
			ips = append(ips, a.String())
		}
		out = append(out, NetInterface{
			Name:  iface.Name,
			IPs:   ips,
			Flags: iface.Flags.String(),
		})
	}
	return out
}

func defaultIfEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
