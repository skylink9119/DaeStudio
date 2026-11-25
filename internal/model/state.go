package model

import "time"

// AppState represents the complete persisted configuration & runtime metadata of daed-next.
type AppState struct {
	Version       string         `json:"version"`
	Revision      string         `json:"revision"`
	UpdatedAt     string         `json:"updatedAt"`
	Auth          AuthConfig     `json:"auth"`
	Global        GlobalConfig   `json:"global"`
	Subscriptions []Subscription `json:"subscriptions"`
	Nodes         []Node         `json:"nodes"`
	Groups        []Group        `json:"groups"`
	DNS           DNSConfig      `json:"dns"`
	Routing       RoutingConfig  `json:"routing"`
	Snapshots     []Snapshot     `json:"snapshots"`
}

type AuthConfig struct {
	Enabled      bool   `json:"enabled"`
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash,omitempty"`
}

type GlobalConfig struct {
	WanInterface              string   `json:"wanInterface"`
	LanInterfaces             []string `json:"lanInterfaces"`
	DialMode                  string   `json:"dialMode"`
	LogLevel                  string   `json:"logLevel"`
	TproxyPort                int      `json:"tproxyPort"`
	TproxyPortProtect         bool     `json:"tproxyPortProtect"`
	SoMarkFromDae             int      `json:"soMarkFromDae"`
	AllowInsecure             bool     `json:"allowInsecure"`
	AutoConfigKernelParameter bool     `json:"autoConfigKernelParameter"`
	CheckInterval             string   `json:"checkInterval"`
	CheckTolerance            string   `json:"checkTolerance"`
	TcpCheckUrl               string   `json:"tcpCheckUrl"`
	TcpCheckHttpMethod        string   `json:"tcpCheckHttpMethod"`
	UdpCheckDns               string   `json:"udpCheckDns"`
	SniffingTimeout           string   `json:"sniffingTimeout"`
	TlsImplementation         string   `json:"tlsImplementation"`
	UtlsImitate               string   `json:"utlsImitate"`
	Mptcp                     bool     `json:"mptcp"`
	DisableWaitingNetwork     bool     `json:"disableWaitingNetwork"`
}

type Subscription struct {
	ID                string `json:"id"`
	Tag               string `json:"tag"`
	Name              string `json:"name"`
	URL               string `json:"url"`
	Format            string `json:"format"` // auto, uri, base64, clash, singbox
	UserAgent         string `json:"userAgent"`
	Enabled           bool   `json:"enabled"`
	AutoUpdateHours   int    `json:"autoUpdateHours"`
	ExcludeKeywords   string `json:"excludeKeywords"`
	PersistConverted  bool   `json:"persistConverted"` // when true, emit converted URIs if subscription isn't natively supported by dae
	NodeCount         int    `json:"nodeCount"`
	UsedUploadBytes   int64  `json:"usedUploadBytes"`
	UsedDownloadBytes int64  `json:"usedDownloadBytes"`
	TotalTrafficBytes int64  `json:"totalTrafficBytes"`
	ExpireUnix        int64  `json:"expireUnix"`
	UpdatedAt         string `json:"updatedAt"`
	LastError         string `json:"lastError,omitempty"`
}

type Node struct {
	ID        string `json:"id"`
	Tag       string `json:"tag"`
	Name      string `json:"name"`
	Protocol  string `json:"protocol"` // vless, vmess, trojan, ss, hysteria2, tuic, socks5, http
	Address   string `json:"address"`
	Port      int    `json:"port"`
	URI       string `json:"uri"`
	SubID     string `json:"subId"`  // empty for manual custom nodes
	SubTag    string `json:"subTag"` // empty for manual custom nodes
	Region    string `json:"region"` // HK, JP, SG, US, TW, KR, DE, UK, OTHER
	Enabled   bool   `json:"enabled"`
	LatencyMs int    `json:"latencyMs"` // -1 = untested, 0 = timeout/error, >0 = ms
	Alive     bool   `json:"alive"`
	CheckedAt string `json:"checkedAt,omitempty"`
}

type GroupFilterRule struct {
	SubTags         []string `json:"subTags"`
	SubIDs          []string `json:"subIds,omitempty"`
	Regions         []string `json:"regions"`
	IncludeKeywords []string `json:"includeKeywords"`
	ExcludeKeywords []string `json:"excludeKeywords"`
	IncludeRegex    string   `json:"includeRegex"`
	ExcludeRegex    string   `json:"excludeRegex"`
	ManualNodeTags  []string `json:"manualNodeTags"`
}

type Group struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	Enabled            bool            `json:"enabled"`
	Policy             string          `json:"policy"` // min_moving_avg, min, min_avg10, random, fixed
	PinnedNodeID       string          `json:"pinnedNodeId,omitempty"`
	PinnedNodeTag      string          `json:"pinnedNodeTag,omitempty"`
	FixedIndex         int             `json:"fixedIndex"`
	Mode               string          `json:"mode"` // smart, raw
	Filter             GroupFilterRule `json:"filter"`
	RawFilterDSL       string          `json:"rawFilterDsl"`
	TcpCheckUrl        string          `json:"tcpCheckUrl,omitempty"`
	TcpCheckHttpMethod string          `json:"tcpCheckHttpMethod,omitempty"`
	UdpCheckDns        string          `json:"udpCheckDns,omitempty"`
	CheckInterval      string          `json:"checkInterval,omitempty"`
	CheckTolerance     string          `json:"checkTolerance,omitempty"`
}

type DNSUpstream struct {
	Tag  string `json:"tag"`
	URL  string `json:"url"`
	Note string `json:"note,omitempty"`
}

type DNSRule struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Expr    string `json:"expr"`
	Target  string `json:"target"`
	Comment string `json:"comment,omitempty"`
}

type DNSConfig struct {
	Mode             string        `json:"mode"` // visual, dsl
	Bind             string        `json:"bind,omitempty"`
	IpVersionPrefer  int           `json:"ipVersionPrefer,omitempty"`
	Upstreams        []DNSUpstream `json:"upstreams"`
	RequestRules     []DNSRule     `json:"requestRules"`
	RequestFallback  string        `json:"requestFallback"`
	ResponseRules    []DNSRule     `json:"responseRules"`
	ResponseFallback string        `json:"responseFallback"`
	RawDSL           string        `json:"rawDsl"`
}

type RoutingCondition struct {
	Negate bool   `json:"negate"`
	Field  string `json:"field"` // domain, dip, sip, dport, sport, l4proto, pname, mac, qname, dscp, mark
	Value  string `json:"value"`
}

type RoutingRule struct {
	ID         string             `json:"id"`
	Enabled    bool               `json:"enabled"`
	Name       string             `json:"name"`
	Category   string             `json:"category"` // system, direct, ai, media, game, block, custom
	Conditions []RoutingCondition `json:"conditions"`
	RawExpr    string             `json:"rawExpr,omitempty"`
	Outbound   string             `json:"outbound"` // direct, must_direct, block, or group name
}

type RoutingConfig struct {
	Mode     string        `json:"mode"` // visual, dsl
	Rules    []RoutingRule `json:"rules"`
	Fallback string        `json:"fallback"`
	RawDSL   string        `json:"rawDsl"`
}

type Snapshot struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
	ConfigDAE string `json:"configDae"`
	StateJSON string `json:"stateJson,omitempty"`
}

// Clone returns a deep copy of AppState so concurrent readers and writers never share slice backing arrays.
func (st AppState) Clone() AppState {
	cp := st
	cp.Global.LanInterfaces = append([]string(nil), st.Global.LanInterfaces...)
	cp.Subscriptions = append([]Subscription(nil), st.Subscriptions...)
	cp.Nodes = append([]Node(nil), st.Nodes...)

	if st.Groups != nil {
		cp.Groups = make([]Group, len(st.Groups))
		for i, g := range st.Groups {
			cg := g
			cg.Filter.SubTags = append([]string(nil), g.Filter.SubTags...)
			cg.Filter.SubIDs = append([]string(nil), g.Filter.SubIDs...)
			cg.Filter.Regions = append([]string(nil), g.Filter.Regions...)
			cg.Filter.IncludeKeywords = append([]string(nil), g.Filter.IncludeKeywords...)
			cg.Filter.ExcludeKeywords = append([]string(nil), g.Filter.ExcludeKeywords...)
			cg.Filter.ManualNodeTags = append([]string(nil), g.Filter.ManualNodeTags...)
			cp.Groups[i] = cg
		}
	}

	cp.DNS.Upstreams = append([]DNSUpstream(nil), st.DNS.Upstreams...)
	cp.DNS.RequestRules = append([]DNSRule(nil), st.DNS.RequestRules...)
	cp.DNS.ResponseRules = append([]DNSRule(nil), st.DNS.ResponseRules...)

	if st.Routing.Rules != nil {
		cp.Routing.Rules = make([]RoutingRule, len(st.Routing.Rules))
		for i, r := range st.Routing.Rules {
			cr := r
			cr.Conditions = append([]RoutingCondition(nil), r.Conditions...)
			cp.Routing.Rules[i] = cr
		}
	}

	cp.Snapshots = append([]Snapshot(nil), st.Snapshots...)
	return cp
}

// DefaultState creates an out-of-the-box rich default state ready to use or preview.
func DefaultState() AppState {
	now := time.Now().Format(time.RFC3339)
	return AppState{
		Version:   "1.0.0",
		UpdatedAt: now,
		Auth: AuthConfig{
			Enabled:  false,
			Username: "admin",
		},
		Global: GlobalConfig{
			WanInterface:              "auto",
			LanInterfaces:             []string{"br-lan"},
			DialMode:                  "domain++",
			LogLevel:                  "info",
			TproxyPort:                12345,
			TproxyPortProtect:         true,
			SoMarkFromDae:             0,
			AllowInsecure:             false,
			AutoConfigKernelParameter: true,
			CheckInterval:             "30s",
			CheckTolerance:            "50ms",
			TcpCheckUrl:               "http://cp.cloudflare.com,1.1.1.1,2606:4700:4700::1111",
			TcpCheckHttpMethod:        "HEAD",
			UdpCheckDns:               "dns.google:53,8.8.8.8,2001:4860:4860::8888",
			SniffingTimeout:           "100ms",
			TlsImplementation:         "tls",
			UtlsImitate:               "chrome_auto",
			Mptcp:                     false,
			DisableWaitingNetwork:     false,
		},
		Subscriptions: []Subscription{
			{
				ID:                "sub-main",
				Tag:               "airport_main",
				Name:              "主力高速订阅 (多格式自动识别)",
				URL:               "https://sub.example.com/api/v1/client/subscribe?token=demo",
				Format:            "auto",
				UserAgent:         "clash.meta",
				Enabled:           true,
				AutoUpdateHours:   6,
				ExcludeKeywords:   "剩余流量|套餐到期|官网|重置",
				PersistConverted:  false,
				NodeCount:         6,
				UsedUploadBytes:   12884901888,
				UsedDownloadBytes: 79456894976,
				TotalTrafficBytes: 536870912000,
				ExpireUnix:        time.Now().AddDate(0, 3, 15).Unix(),
				UpdatedAt:         now,
			},
		},
		Nodes: []Node{
			{
				ID: "node-hk-01", Tag: "hk_iepl_01", Name: "🇭🇰 香港 IEPL 专线 01",
				Protocol: "vless", Address: "1.1.1.1", Port: 443,
				URI:   "vless://00000000-0000-0000-0000-000000000001@1.1.1.1:443?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk=demo#HK-IEPL-01",
				SubID: "sub-main", SubTag: "airport_main", Region: "HK", Enabled: true, LatencyMs: 28, Alive: true, CheckedAt: now,
			},
			{
				ID: "node-hk-02", Tag: "hk_hy2_02", Name: "🇭🇰 香港 Hysteria2 极速 02",
				Protocol: "hysteria2", Address: "1.0.0.1", Port: 8443,
				URI:   "hysteria2://demopass@1.0.0.1:8443?sni=bing.com&insecure=0#HK-Hy2-02",
				SubID: "sub-main", SubTag: "airport_main", Region: "HK", Enabled: true, LatencyMs: 31, Alive: true, CheckedAt: now,
			},
			{
				ID: "node-jp-01", Tag: "jp_tokyo_01", Name: "🇯🇵 日本 东京 BGP 01",
				Protocol: "vless", Address: "8.8.8.8", Port: 443,
				URI:   "vless://00000000-0000-0000-0000-000000000002@8.8.8.8:443?encryption=none&security=tls&sni=example.jp#JP-Tokyo-01",
				SubID: "sub-main", SubTag: "airport_main", Region: "JP", Enabled: true, LatencyMs: 54, Alive: true, CheckedAt: now,
			},
			{
				ID: "node-sg-01", Tag: "sg_狮城_01", Name: "🇸🇬 新加坡 狮城直连 01",
				Protocol: "trojan", Address: "8.8.4.4", Port: 443,
				URI:   "trojan://demopass@8.8.4.4:443?sni=example.sg#SG-Direct-01",
				SubID: "sub-main", SubTag: "airport_main", Region: "SG", Enabled: true, LatencyMs: 65, Alive: true, CheckedAt: now,
			},
			{
				ID: "node-us-01", Tag: "us_gpt_01", Name: "🇺🇸 美国 硅谷 AI 原生解锁 01",
				Protocol: "vless", Address: "208.67.222.222", Port: 443,
				URI:   "vless://00000000-0000-0000-0000-000000000003@208.67.222.222:443?encryption=none&security=reality&sni=apple.com#US-AI-01",
				SubID: "sub-main", SubTag: "airport_main", Region: "US", Enabled: true, LatencyMs: 142, Alive: true, CheckedAt: now,
			},
			{
				ID: "node-tw-01", Tag: "tw_hinet_01", Name: "🇹🇼 台湾 动画疯解锁 01",
				Protocol: "ss", Address: "9.9.9.9", Port: 8388,
				URI:   "ss://MjAyMi1ibGFrZTMtYWVzLTI1Ni1nY206ZGVtb2tleQ==@9.9.9.9:8388#TW-HiNet-01",
				SubID: "sub-main", SubTag: "airport_main", Region: "TW", Enabled: true, LatencyMs: 46, Alive: true, CheckedAt: now,
			},
			{
				ID: "node-custom-01", Tag: "self_hosted_jp", Name: "🛠️ 自建日本落地 VLESS",
				Protocol: "vless", Address: "1.1.1.1", Port: 443,
				URI:   "vless://00000000-0000-0000-0000-000000000099@1.1.1.1:443?encryption=none&security=reality&sni=yahoo.co.jp#Self-JP",
				SubID: "", SubTag: "", Region: "JP", Enabled: true, LatencyMs: 49, Alive: true, CheckedAt: now,
			},
		},
		Groups: []Group{
			{
				ID: "grp-proxy", Name: "proxy", Description: "主力默认出海策略组（自动优选移动平均最低延迟）",
				Enabled: true, Policy: "min_moving_avg", Mode: "smart",
				Filter: GroupFilterRule{
					SubTags:         []string{"airport_main"},
					Regions:         []string{},
					ExcludeKeywords: []string{"ExpireAt", "剩余流量", "官网"},
					ManualNodeTags:  []string{"self_hosted_jp"},
				},
			},
			{
				ID: "grp-ai", Name: "ai_service", Description: "OpenAI / Claude / Gemini 专用组（自动排除香港节点）",
				Enabled: true, Policy: "min_moving_avg", Mode: "smart",
				Filter: GroupFilterRule{
					SubTags:         []string{"airport_main"},
					Regions:         []string{"US", "JP", "SG", "TW"},
					ExcludeKeywords: []string{"HK", "香港"},
				},
			},
			{
				ID: "grp-media", Name: "streaming", Description: "Netflix / YouTube / Disney+ 港台日新流媒体高速组",
				Enabled: true, Policy: "min", Mode: "smart",
				Filter: GroupFilterRule{
					SubTags: []string{"airport_main"},
					Regions: []string{"HK", "TW", "JP", "SG"},
				},
			},
		},
		DNS: DNSConfig{
			Mode: "visual",
			Upstreams: []DNSUpstream{
				{Tag: "alidns", URL: "udp://223.5.5.5:53", Note: "阿里公共 DNS（国内极速解析）"},
				{Tag: "dnspod", URL: "udp://119.29.29.29:53", Note: "腾讯 DNSPod"},
				{Tag: "googledns", URL: "tcp+udp://8.8.8.8:53", Note: "Google 无污染海外 DNS"},
				{Tag: "cloudflare", URL: "tcp+udp://1.1.1.1:53", Note: "Cloudflare 海外 DNS"},
			},
			RequestRules: []DNSRule{
				{ID: "dns-req-1", Enabled: true, Expr: "qname(geosite:category-ads-all)", Target: "reject", Comment: "DNS 级广告与追踪域名拦截"},
				{ID: "dns-req-2", Enabled: true, Expr: "qname(geosite:cn)", Target: "alidns", Comment: "国内域名使用阿里 DNS 解析"},
			},
			RequestFallback: "googledns",
			ResponseRules: []DNSRule{
				{ID: "dns-resp-1", Enabled: true, Expr: "upstream(googledns)", Target: "accept", Comment: "海外可信上游直接放行"},
				{ID: "dns-resp-2", Enabled: true, Expr: "ip(geoip:private) && !qname(geosite:cn)", Target: "googledns", Comment: "防 DNS 污染回退重查"},
			},
			ResponseFallback: "accept",
		},
		Routing: RoutingConfig{
			Mode:     "visual",
			Fallback: "proxy",
			Rules: []RoutingRule{
				{
					ID: "rt-1", Enabled: true, Name: "绕过系统核心网络进程防回环", Category: "system",
					Conditions: []RoutingCondition{{Field: "pname", Value: "NetworkManager, systemd-resolved, dnsmasq"}},
					Outbound:   "must_direct",
				},
				{
					ID: "rt-2", Enabled: true, Name: "组播与广播地址直连", Category: "system",
					Conditions: []RoutingCondition{{Field: "dip", Value: "224.0.0.0/3, 'ff00::/8'"}},
					Outbound:   "direct",
				},
				{
					ID: "rt-3", Enabled: true, Name: "局域网私有 IP 直连", Category: "direct",
					Conditions: []RoutingCondition{{Field: "dip", Value: "geoip:private"}},
					Outbound:   "direct",
				},
				{
					ID: "rt-4", Enabled: false, Name: "屏蔽海外 QUIC (UDP 443) 提升分流稳定性", Category: "block",
					Conditions: []RoutingCondition{
						{Field: "l4proto", Value: "udp"},
						{Field: "dport", Value: "443"},
						{Negate: true, Field: "dip", Value: "geoip:cn"},
					},
					Outbound: "block",
				},
				{
					ID: "rt-5", Enabled: true, Name: "AI 服务分流 (OpenAI / Anthropic / Gemini)", Category: "ai",
					Conditions: []RoutingCondition{{Field: "domain", Value: "geosite:openai, geosite:anthropic, geosite:google-gemini, keyword:claude, keyword:perplexity"}},
					Outbound:   "ai_service",
				},
				{
					ID: "rt-6", Enabled: true, Name: "国际流媒体走流媒体策略组", Category: "media",
					Conditions: []RoutingCondition{{Field: "domain", Value: "geosite:netflix, geosite:disney, geosite:youtube, geosite:bahamut"}},
					Outbound:   "streaming",
				},
				{
					ID: "rt-7", Enabled: true, Name: "中国大陆域名直连", Category: "direct",
					Conditions: []RoutingCondition{{Field: "domain", Value: "geosite:cn"}},
					Outbound:   "direct",
				},
				{
					ID: "rt-8", Enabled: true, Name: "中国大陆目标 IP 直连", Category: "direct",
					Conditions: []RoutingCondition{{Field: "dip", Value: "geoip:cn"}},
					Outbound:   "direct",
				},
			},
		},
		Snapshots: []Snapshot{},
	}
}
