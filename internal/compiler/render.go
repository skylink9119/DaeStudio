package compiler

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"daed-next/internal/model"
)

var regionRegexMap = map[string]string{
	"HK": "HK|Hong\\s*Kong|香港|港",
	"TW": "TW|Taiwan|台湾|台|新北|台北|彰化",
	"JP": "JP|Japan|日本|东京|大阪|埼玉",
	"SG": "SG|Singapore|新加坡|狮城",
	"US": "US|United\\s*States|美国|美|硅谷|洛杉矶|西雅图|圣何塞",
	"KR": "KR|Korea|韩国|首尔|韩",
	"DE": "DE|Germany|德国|法兰克福",
	"UK": "UK|GB|Britain|英国|伦敦",
}

// EnabledNodes filters st.Nodes down to enabled nodes whose parent subscription (if any) is also enabled.
func EnabledNodes(st model.AppState) []model.Node {
	enabledSubs := map[string]model.Subscription{}
	for _, s := range st.Subscriptions {
		if s.Enabled {
			enabledSubs[s.ID] = s
		}
	}
	var out []model.Node
	for _, n := range st.Nodes {
		if !n.Enabled || strings.TrimSpace(n.URI) == "" {
			continue
		}
		if n.SubID != "" {
			sub, ok := enabledSubs[n.SubID]
			if !ok {
				continue
			}
			n.SubTag = sub.Tag
		}
		out = append(out, n)
	}
	return out
}

// MatchNodesForGroup evaluates a Group's smart filter against the active node pool.
func MatchNodesForGroup(g model.Group, activeNodes []model.Node) []model.Node {
	idxs := matchNodeIndicesForGroup(g, activeNodes)
	matched := make([]model.Node, len(idxs))
	for i, idx := range idxs {
		matched[i] = activeNodes[idx]
	}
	return matched
}

func matchNodeIndicesForGroup(g model.Group, activeNodes []model.Node) []int {
	f := g.Filter
	manualSet := map[string]bool{}
	for _, t := range f.ManualNodeTags {
		if strings.TrimSpace(t) != "" {
			manualSet[strings.TrimSpace(t)] = true
		}
	}
	subIDs := make(map[string]bool)
	for _, id := range f.SubIDs {
		if strings.TrimSpace(id) != "" {
			subIDs[strings.TrimSpace(id)] = true
		}
	}
	subSet := map[string]bool{}
	for _, s := range f.SubTags {
		if strings.TrimSpace(s) != "" {
			subSet[strings.TrimSpace(s)] = true
		}
	}
	regionSet := map[string]bool{}
	var regionRes []*regexp.Regexp
	for _, r := range f.Regions {
		up := strings.ToUpper(strings.TrimSpace(r))
		if up != "" {
			regionSet[up] = true
			if pat, ok := regionRegexMap[up]; ok {
				if re, err := regexp.Compile("(?i)" + pat); err == nil {
					regionRes = append(regionRes, re)
				}
			}
		}
	}

	var incKws, excKws []string
	for _, kw := range f.IncludeKeywords {
		if strings.TrimSpace(kw) != "" {
			incKws = append(incKws, strings.ToLower(strings.TrimSpace(kw)))
		}
	}
	for _, kw := range f.ExcludeKeywords {
		if strings.TrimSpace(kw) != "" {
			excKws = append(excKws, strings.ToLower(strings.TrimSpace(kw)))
		}
	}

	var incRe, excRe *regexp.Regexp
	if strings.TrimSpace(f.IncludeRegex) != "" {
		incRe, _ = regexp.Compile("(?i)" + strings.TrimSpace(f.IncludeRegex))
	}
	if strings.TrimSpace(f.ExcludeRegex) != "" {
		excRe, _ = regexp.Compile("(?i)" + strings.TrimSpace(f.ExcludeRegex))
	}

	hasDynamic := len(subIDs) > 0 || len(subSet) > 0 || len(regionSet) > 0 || len(incKws) > 0 || len(excKws) > 0 || incRe != nil || excRe != nil

	var matched []int
	for idx, n := range activeNodes {
		if manualSet[n.Tag] {
			matched = append(matched, idx)
			continue
		}
		if !hasDynamic && len(manualSet) > 0 {
			continue
		}
		if len(subIDs) > 0 && !subIDs[n.SubID] {
			continue
		}
		if len(subSet) > 0 && !subSet[n.SubTag] {
			continue
		}
		if len(regionSet) > 0 {
			regOK := regionSet[strings.ToUpper(n.Region)]
			if !regOK {
				for _, rre := range regionRes {
					if rre.MatchString(n.Name) {
						regOK = true
						break
					}
				}
			}
			if !regOK {
				continue
			}
		}
		lowName := strings.ToLower(n.Name)
		incOK := true
		for _, kw := range incKws {
			if !strings.Contains(lowName, kw) {
				incOK = false
				break
			}
		}
		if !incOK {
			continue
		}
		excHit := false
		for _, kw := range excKws {
			if strings.Contains(lowName, kw) {
				excHit = true
				break
			}
		}
		if excHit {
			continue
		}
		if incRe != nil && !incRe.MatchString(n.Name) {
			continue
		}
		if excRe != nil && excRe.MatchString(n.Name) {
			continue
		}
		matched = append(matched, idx)
	}
	return matched
}

// Node aliases are allocated once and shared by the node block and group filters.
func nodeAliases(nodes []model.Node) []string {
	aliases := make([]string, len(nodes))
	used := make(map[string]bool)
	for i, node := range nodes {
		base := sanitizeIdentifier(node.Tag)
		if base == "" {
			base = "node"
		}
		alias := base
		for n := 2; used[alias]; n++ {
			alias = fmt.Sprintf("%s_%d", base, n)
		}
		aliases[i] = alias
		used[alias] = true
	}
	return aliases
}

// selectedNode returns the exact fixed node, including support for older saved tags.
func selectedNode(group model.Group, nodes []model.Node) (int, error) {
	found := -1
	for i, node := range nodes {
		if group.PinnedNodeID != "" && group.PinnedNodeID != node.ID ||
			group.PinnedNodeID == "" && group.PinnedNodeTag != node.Tag {
			continue
		}
		if found >= 0 {
			return -1, fmt.Errorf("策略组 %s 的固定节点有重名，请重新选择节点", group.Name)
		}
		found = i
	}
	if found < 0 {
		return -1, fmt.Errorf("策略组 %s 固定的节点已停用或不存在，请选择新节点或切换自动模式", group.Name)
	}
	return found, nil
}

func hasPin(group model.Group) bool {
	return group.Policy == "fixed" && (group.PinnedNodeID != "" || group.PinnedNodeTag != "")
}

// ValidateOutbounds catches missing/disabled group references and empty managed groups
// that dae either rejects with opaque errors or accepts syntactically without usable nodes.
// runtimeState keeps the public template editable while no nodes are available.
func runtimeState(st model.AppState) model.AppState {
	if !st.DirectWithoutNodes || len(EnabledNodes(st)) > 0 {
		return st
	}
	st.Groups = nil
	st.Routing = model.RoutingConfig{Mode: "visual", Fallback: "direct"}
	st.DNS = model.DNSConfig{
		Mode: "visual", Bind: st.DNS.Bind, IpVersionPrefer: st.DNS.IpVersionPrefer,
		Upstreams:       []model.DNSUpstream{{Tag: "alidns", URL: "udp://223.5.5.5:53"}},
		RequestFallback: "alidns", ResponseFallback: "accept",
	}
	return st
}

func ValidateOutbounds(st model.AppState) error {
	nodes := EnabledNodes(st)
	builtinOutbounds := map[string]bool{
		"direct":      true,
		"must_direct": true,
		"block":       true,
	}
	enabledGroups := make(map[string]bool, len(st.Groups))
	for _, group := range st.Groups {
		if group.Enabled {
			enabledGroups[sanitizeIdentifier(group.Name)] = true
			enabledGroups[strings.TrimSpace(group.Name)] = true
		}
	}

	referenced := map[string]bool{}
	var orderedRefs []string
	addRef := func(ob string) {
		ob = strings.TrimSpace(ob)
		if ob == "" || referenced[ob] {
			return
		}
		referenced[ob] = true
		orderedRefs = append(orderedRefs, ob)
	}

	if st.Routing.Mode == "dsl" && strings.TrimSpace(st.Routing.RawDSL) != "" {
		rawRouting := unwrapBlockBody(st.Routing.RawDSL, "routing")
		pattern := regexp.MustCompile(`(?:->|\bfallback\s*:)\s*([A-Za-z0-9_-]+)`)
		for _, line := range strings.Split(rawRouting, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, match := range pattern.FindAllStringSubmatch(line, -1) {
				addRef(match[1])
			}
		}
	} else {
		addRef(st.Routing.Fallback)
		for _, rule := range st.Routing.Rules {
			if rule.Enabled {
				addRef(rule.Outbound)
			}
		}
	}

	for _, ob := range orderedRefs {
		if !builtinOutbounds[ob] && !enabledGroups[ob] && !enabledGroups[sanitizeIdentifier(ob)] {
			return fmt.Errorf("分流规则引用了不存在或已停用的策略组 %q，请先将关联规则调整到有效出口", ob)
		}
	}

	if st.DirectWithoutNodes && len(nodes) == 0 {
		return nil
	}

	for _, group := range st.Groups {
		if !group.Enabled {
			continue
		}
		if hasPin(group) {
			if _, err := selectedNode(group, nodes); err != nil {
				return err
			}
			continue
		}
		if group.Mode == "raw" && strings.TrimSpace(group.RawFilterDSL) != "" {
			continue
		}
		if !referenced[sanitizeIdentifier(group.Name)] {
			continue
		}
		count := len(MatchNodesForGroup(group, nodes))
		if count == 0 {
			return fmt.Errorf("策略组 %s 没有匹配的启用节点，请选择节点或订阅源后再应用", group.Name)
		}
		if group.Policy == "fixed" && (group.FixedIndex < 0 || group.FixedIndex >= count) {
			return fmt.Errorf("策略组 %s 的固定节点序号超出范围，请重新选择", group.Name)
		}
	}
	return nil
}

func renderGroupFilter(group model.Group, nodes []model.Node, aliases []string) []string {
	if hasPin(group) {
		if i, err := selectedNode(group, nodes); err == nil {
			return []string{fmt.Sprintf("filter: name(%s)", aliases[i])}
		}
		return []string{"filter: name(regex: '^$')"}
	}
	if group.Mode == "raw" && strings.TrimSpace(group.RawFilterDSL) != "" {
		var lines []string
		for _, line := range strings.Split(group.RawFilterDSL, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "filter:") || strings.HasPrefix(trimmed, "#") {
				lines = append(lines, trimmed)
			} else {
				lines = append(lines, "filter: "+trimmed)
			}
		}
		return lines
	}
	// Evaluate against aliases by exact slice index.
	matchedIdxs := matchNodeIndicesForGroup(group, nodes)
	if len(matchedIdxs) == 0 {
		return []string{"filter: name(regex: '^$')"}
	}
	if len(matchedIdxs) == len(nodes) {
		return nil
	}
	tags := make([]string, 0, len(matchedIdxs))
	for _, idx := range matchedIdxs {
		tags = append(tags, aliases[idx])
	}
	return []string{fmt.Sprintf("filter: name(%s)", strings.Join(tags, ", "))}
}

// RenderRoutingRuleExpr renders a single visual routing rule into dae DSL condition -> outbound.
func RenderRoutingRuleExpr(r model.RoutingRule) string {
	var condStr string
	if strings.TrimSpace(r.RawExpr) != "" && len(r.Conditions) == 0 {
		condStr = strings.TrimSpace(r.RawExpr)
	} else {
		var parts []string
		for _, c := range r.Conditions {
			val := strings.TrimSpace(c.Value)
			field := strings.TrimSpace(c.Field)
			if field == "" || val == "" {
				continue
			}
			prefix := ""
			if c.Negate {
				prefix = "!"
			}
			parts = append(parts, fmt.Sprintf("%s%s(%s)", prefix, field, val))
		}
		condStr = strings.Join(parts, " && ")
	}
	if condStr == "" {
		return ""
	}
	outbound := strings.TrimSpace(r.Outbound)
	if outbound == "" {
		outbound = "direct"
	}
	return fmt.Sprintf("%s -> %s", condStr, outbound)
}

// RenderConfig compiles the full AppState into a standard, well-commented config.dae document.
func RenderConfig(st model.AppState) string {
	isDirectFallback := st.DirectWithoutNodes && len(EnabledNodes(st)) == 0
	st = runtimeState(st)
	var b strings.Builder
	activeNodes := EnabledNodes(st)
	aliases := nodeAliases(activeNodes)
	nodeDomains := collectNodeServerDomains(activeNodes)

	b.WriteString("# ==============================================================================\n")
	b.WriteString("# Generated by DaeStudio (daed-next) — Modern High-Performance dae Control Plane\n")
	b.WriteString("# Validate syntax anytime with: dae validate -c /etc/dae/config.dae\n")
	if isDirectFallback {
		b.WriteString("# [DirectWithoutNodes] 当前无可用代理节点，运行配置临时启用安全直连模式\n")
	}
	b.WriteString("# ==============================================================================\n\n")

	// 1. Global Block
	g := st.Global
	b.WriteString("global {\n")
	if strings.TrimSpace(g.WanInterface) != "" {
		b.WriteString(fmt.Sprintf("    wan_interface: %s\n", strings.TrimSpace(g.WanInterface)))
	}
	if len(g.LanInterfaces) > 0 {
		var lans []string
		for _, l := range g.LanInterfaces {
			if strings.TrimSpace(l) != "" {
				lans = append(lans, strings.TrimSpace(l))
			}
		}
		if len(lans) > 0 {
			b.WriteString(fmt.Sprintf("    lan_interface: %s\n", strings.Join(lans, ",")))
		}
	}
	if g.TproxyPort > 0 {
		b.WriteString(fmt.Sprintf("    tproxy_port: %d\n", g.TproxyPort))
	}
	b.WriteString(fmt.Sprintf("    tproxy_port_protect: %t\n", g.TproxyPortProtect))
	if g.SoMarkFromDae > 0 {
		b.WriteString(fmt.Sprintf("    so_mark_from_dae: %d\n", g.SoMarkFromDae))
	}
	if g.LogLevel != "" {
		b.WriteString(fmt.Sprintf("    log_level: %s\n", g.LogLevel))
	}
	if g.DialMode != "" {
		b.WriteString(fmt.Sprintf("    dial_mode: %s\n", g.DialMode))
	}
	b.WriteString(fmt.Sprintf("    allow_insecure: %t\n", g.AllowInsecure))
	b.WriteString(fmt.Sprintf("    auto_config_kernel_parameter: %t\n", g.AutoConfigKernelParameter))
	if g.CheckInterval != "" {
		b.WriteString(fmt.Sprintf("    check_interval: %s\n", g.CheckInterval))
	}
	if g.CheckTolerance != "" {
		b.WriteString(fmt.Sprintf("    check_tolerance: %s\n", g.CheckTolerance))
	}
	if g.TcpCheckUrl != "" {
		b.WriteString(fmt.Sprintf("    tcp_check_url: '%s'\n", escapeSingleQuote(g.TcpCheckUrl)))
	}
	if g.TcpCheckHttpMethod != "" {
		b.WriteString(fmt.Sprintf("    tcp_check_http_method: %s\n", g.TcpCheckHttpMethod))
	}
	if g.UdpCheckDns != "" {
		b.WriteString(fmt.Sprintf("    udp_check_dns: '%s'\n", escapeSingleQuote(g.UdpCheckDns)))
	}
	if g.SniffingTimeout != "" {
		b.WriteString(fmt.Sprintf("    sniffing_timeout: %s\n", g.SniffingTimeout))
	}
	if g.TlsImplementation != "" {
		b.WriteString(fmt.Sprintf("    tls_implementation: %s\n", g.TlsImplementation))
	}
	if g.TlsImplementation == "utls" && g.UtlsImitate != "" {
		b.WriteString(fmt.Sprintf("    utls_imitate: %s\n", g.UtlsImitate))
	}
	if g.Mptcp {
		b.WriteString("    mptcp: true\n")
	}
	b.WriteString(fmt.Sprintf("    disable_waiting_network: %t\n", g.DisableWaitingNetwork))
	b.WriteString("}\n\n")

	// 2. Subscription Block (subscriptions are fetched & normalized by DaeStudio into node {} for fast offline reload)
	b.WriteString("subscription {\n}\n\n")

	// 3. Node Block (all enabled manual + subscription nodes with unique tags)
	b.WriteString("node {\n")
	for i, n := range activeNodes {
		fmt.Fprintf(&b, "    %s: '%s'\n", aliases[i], escapeSingleQuote(strings.TrimSpace(n.URI)))
	}
	b.WriteString("}\n\n")

	// 4. DNS Block
	b.WriteString("dns {\n")
	if st.DNS.Mode == "dsl" && strings.TrimSpace(st.DNS.RawDSL) != "" {
		for _, line := range strings.Split(strings.TrimSpace(unwrapBlockBody(st.DNS.RawDSL, "dns")), "\n") {
			b.WriteString("    " + line + "\n")
		}
	} else {
		if strings.TrimSpace(st.DNS.Bind) != "" {
			b.WriteString(fmt.Sprintf("    bind: '%s'\n", escapeSingleQuote(strings.TrimSpace(st.DNS.Bind))))
		}
		if st.DNS.IpVersionPrefer > 0 {
			b.WriteString(fmt.Sprintf("    ipversion_prefer: %d\n", st.DNS.IpVersionPrefer))
		}
		b.WriteString("    upstream {\n")
		for _, up := range st.DNS.Upstreams {
			tag := sanitizeIdentifier(up.Tag)
			if tag != "" && strings.TrimSpace(up.URL) != "" {
				if up.Note != "" {
					b.WriteString(fmt.Sprintf("        # %s\n", up.Note))
				}
				b.WriteString(fmt.Sprintf("        %s: '%s'\n", tag, escapeSingleQuote(strings.TrimSpace(up.URL))))
			}
		}
		b.WriteString("    }\n")
		b.WriteString("    routing {\n")
		b.WriteString("        request {\n")

		// Auto-bypass proxy node entry domains via domestic DNS to prevent bootstrap deadlock!
		directDNSTag := pickDirectDNSTag(st.DNS)
		if len(nodeDomains) > 0 && directDNSTag != "" {
			fullList := make([]string, len(nodeDomains))
			for i, d := range nodeDomains {
				fullList[i] = "full:" + d
			}
			b.WriteString("            # [Auto:NodeDomains] 代理节点入口域名走直连 DNS 解析（防死锁）\n")
			b.WriteString(fmt.Sprintf("            qname(%s) -> %s\n", strings.Join(fullList, ", "), directDNSTag))
		}

		for _, r := range st.DNS.RequestRules {
			if !r.Enabled || strings.TrimSpace(r.Expr) == "" {
				continue
			}
			if r.Comment != "" {
				b.WriteString(fmt.Sprintf("            # %s\n", r.Comment))
			}
			b.WriteString(fmt.Sprintf("            %s -> %s\n", strings.TrimSpace(r.Expr), strings.TrimSpace(r.Target)))
		}
		reqFb := strings.TrimSpace(st.DNS.RequestFallback)
		if reqFb == "" {
			reqFb = "googledns"
		}
		b.WriteString(fmt.Sprintf("            fallback: %s\n", reqFb))
		b.WriteString("        }\n")
		if len(st.DNS.ResponseRules) > 0 {
			b.WriteString("        response {\n")
			for _, r := range st.DNS.ResponseRules {
				if !r.Enabled || strings.TrimSpace(r.Expr) == "" {
					continue
				}
				if r.Comment != "" {
					b.WriteString(fmt.Sprintf("            # %s\n", r.Comment))
				}
				b.WriteString(fmt.Sprintf("            %s -> %s\n", strings.TrimSpace(r.Expr), strings.TrimSpace(r.Target)))
			}
			respFb := strings.TrimSpace(st.DNS.ResponseFallback)
			if respFb == "" {
				respFb = "accept"
			}
			b.WriteString(fmt.Sprintf("            fallback: %s\n", respFb))
			b.WriteString("        }\n")
		}
		b.WriteString("    }\n")
	}
	b.WriteString("}\n\n")

	// 5. Group Block
	b.WriteString("group {\n")
	for _, grp := range st.Groups {
		if !grp.Enabled {
			continue
		}
		name := sanitizeIdentifier(grp.Name)
		if name == "" {
			continue
		}
		if grp.Description != "" {
			b.WriteString(fmt.Sprintf("    # %s\n", grp.Description))
		}
		b.WriteString(fmt.Sprintf("    %s {\n", name))
		for _, fl := range renderGroupFilter(grp, activeNodes, aliases) {
			b.WriteString("        " + fl + "\n")
		}
		policy := strings.TrimSpace(grp.Policy)
		if policy == "" {
			policy = "min_moving_avg"
		}
		if policy == "fixed" {
			index := grp.FixedIndex
			if hasPin(grp) {
				index = 0
			}
			policy = fmt.Sprintf("fixed(%d)", index)
		}
		b.WriteString(fmt.Sprintf("        policy: %s\n", policy))
		if grp.TcpCheckUrl != "" {
			b.WriteString(fmt.Sprintf("        tcp_check_url: '%s'\n", escapeSingleQuote(grp.TcpCheckUrl)))
		}
		if grp.TcpCheckHttpMethod != "" {
			b.WriteString(fmt.Sprintf("        tcp_check_http_method: %s\n", grp.TcpCheckHttpMethod))
		}
		if grp.UdpCheckDns != "" {
			b.WriteString(fmt.Sprintf("        udp_check_dns: '%s'\n", escapeSingleQuote(grp.UdpCheckDns)))
		}
		if grp.CheckInterval != "" {
			b.WriteString(fmt.Sprintf("        check_interval: %s\n", grp.CheckInterval))
		}
		if grp.CheckTolerance != "" {
			b.WriteString(fmt.Sprintf("        check_tolerance: %s\n", grp.CheckTolerance))
		}
		b.WriteString("    }\n")
	}
	b.WriteString("}\n\n")

	// 6. Routing Block
	b.WriteString("routing {\n")
	if st.Routing.Mode == "dsl" && strings.TrimSpace(st.Routing.RawDSL) != "" {
		for _, line := range strings.Split(strings.TrimSpace(unwrapBlockBody(st.Routing.RawDSL, "routing")), "\n") {
			b.WriteString("    " + line + "\n")
		}
	} else {
		if len(nodeDomains) > 0 {
			fullList := make([]string, len(nodeDomains))
			for i, d := range nodeDomains {
				fullList[i] = "full:" + d
			}
			b.WriteString("    # [Auto:NodeDomains] 代理节点入口域名强制直连（防回环）\n")
			b.WriteString(fmt.Sprintf("    domain(%s) -> must_direct\n", strings.Join(fullList, ", ")))
		}
		for _, r := range st.Routing.Rules {
			expr := RenderRoutingRuleExpr(r)
			if expr == "" {
				continue
			}
			if r.Name != "" {
				b.WriteString(fmt.Sprintf("    # %s\n", r.Name))
			}
			if r.Enabled {
				b.WriteString("    " + expr + "\n")
			} else {
				b.WriteString("    # [Disabled] " + expr + "\n")
			}
		}
		fb := strings.TrimSpace(st.Routing.Fallback)
		if fb == "" {
			fb = "direct"
		}
		b.WriteString(fmt.Sprintf("\n    fallback: %s\n", fb))
	}
	b.WriteString("}\n")

	return b.String()
}

func collectNodeServerDomains(nodes []model.Node) []string {
	seen := map[string]bool{}
	var domains []string
	for _, n := range nodes {
		addr := strings.Trim(strings.TrimSpace(n.Address), "[]")
		if addr == "" || addr == "127.0.0.1" || addr == "localhost" {
			continue
		}
		if net.ParseIP(addr) != nil {
			continue
		}
		low := strings.ToLower(addr)
		if !seen[low] {
			seen[low] = true
			domains = append(domains, low)
		}
	}
	return domains
}

func pickDirectDNSTag(dns model.DNSConfig) string {
	for _, r := range dns.RequestRules {
		if strings.Contains(r.Expr, "geosite:cn") && r.Target != "" && r.Target != "reject" {
			return sanitizeIdentifier(r.Target)
		}
	}
	for _, u := range dns.Upstreams {
		low := strings.ToLower(u.Tag + " " + u.URL)
		if strings.Contains(low, "cn") || strings.Contains(low, "ali") || strings.Contains(low, "223.5.5.5") || strings.Contains(low, "119.29.29.29") || strings.Contains(low, "dnspod") {
			return sanitizeIdentifier(u.Tag)
		}
	}
	for _, u := range dns.Upstreams {
		if u.Tag != dns.RequestFallback {
			return sanitizeIdentifier(u.Tag)
		}
	}
	if len(dns.Upstreams) > 0 {
		return sanitizeIdentifier(dns.Upstreams[0].Tag)
	}
	return ""
}

func escapeSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

func sanitizeIdentifier(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var out strings.Builder
	for i, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (i > 0 && ((r >= '0' && r <= '9') || r == '-')) {
			out.WriteRune(r)
		} else if r >= '0' && r <= '9' && i == 0 {
			out.WriteString("n_")
			out.WriteRune(r)
		} else {
			out.WriteRune('_')
		}
	}
	return out.String()
}
