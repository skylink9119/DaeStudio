package compiler

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"daed-next/internal/model"
	"daed-next/internal/subparser"
)

var (
	condItemRe   = regexp.MustCompile(`^(!?)([a-zA-Z0-9_]+)\((.*)\)$`)
	fixedIndexRe = regexp.MustCompile(`^fixed\(\s*(\d+)\s*\)$`)
	singleNameRe = regexp.MustCompile(`^filter:\s*name\(\s*([A-Za-z0-9_-]+)\s*\)$`)
)

type namedBlock struct {
	Name string
	Body string
}

func isAutoNodeDomainComment(comment string) bool {
	c := strings.TrimSpace(comment)
	return strings.HasPrefix(c, "[Auto:NodeDomains]") ||
		c == "代理节点入口域名强制直连（防回环）" ||
		c == "代理节点入口域名走直连 DNS 解析（防死锁）"
}

// ValidateSyntax performs static structural checks on a config.dae string before invoking dae validate.
func ValidateSyntax(cfg string) []string {
	var errs []string
	depth := 0
	quote := byte(0)
	escaped := false
	lines := strings.Split(cfg, "\n")

	for idx, rawLine := range lines {
		lineNo := idx + 1
		for i := 0; i < len(rawLine); i++ {
			ch := rawLine[i]
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' && quote != 0 {
				escaped = true
				continue
			}
			if quote == 0 && ch == '#' {
				break
			}
			if ch == '\'' || ch == '"' {
				if quote == 0 {
					quote = ch
				} else if quote == ch {
					quote = 0
				}
				continue
			}
			if quote == 0 {
				if ch == '{' {
					depth++
				} else if ch == '}' {
					depth--
					if depth < 0 {
						errs = append(errs, fmt.Sprintf("第 %d 行存在多余的右花括号 '}'", lineNo))
						depth = 0
					}
				}
			}
		}
		if quote != 0 {
			errs = append(errs, fmt.Sprintf("第 %d 行引号 %c 未闭合", lineNo, quote))
			quote = 0
		}
	}
	if depth != 0 {
		errs = append(errs, fmt.Sprintf("花括号层级未闭合（差值 %d）", depth))
	} else {
		if _, ok := extractTopLevelBlocks(cfg)["routing"]; !ok {
			errs = append(errs, "缺少必选的 routing { ... } 路由块")
		}
	}
	return errs
}

// unwrapBlockBody extracts the inner body of `blockName { ... }` if raw contains top-level blocks;
// otherwise returns raw unchanged.
func unwrapBlockBody(raw, blockName string) string {
	if strings.Contains(raw, "{") {
		if blocks := extractTopLevelBlocks(raw); len(blocks) > 0 {
			if body, ok := blocks[blockName]; ok {
				return strings.TrimSpace(body)
			}
		}
	}
	return raw
}

// ParseRoutingDSL parses inner lines of `routing { ... }` into structured RoutingRule items + fallback.
// If a full config.dae or wrapped `routing { ... }` block is passed in, it extracts only the top-level routing block.
func ParseRoutingDSL(dsl string) ([]model.RoutingRule, string) {
	dsl = unwrapBlockBody(dsl, "routing")
	var rules []model.RoutingRule
	fallback := "direct"
	var pendingComment string

	for idx, raw := range strings.Split(dsl, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			commentText := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			if strings.HasPrefix(commentText, "[Disabled]") {
				disabledExpr := strings.TrimSpace(strings.TrimPrefix(commentText, "[Disabled]"))
				if !isAutoNodeDomainComment(pendingComment) {
					if r, ok := parseSingleRoutingLine(disabledExpr, pendingComment, idx+1, false); ok {
						rules = append(rules, r)
						pendingComment = ""
						continue
					}
				}
			}
			pendingComment = commentText
			continue
		}
		line = stripInlineComment(line)
		if strings.HasPrefix(line, "fallback:") {
			fallback = strings.TrimSpace(strings.TrimPrefix(line, "fallback:"))
			pendingComment = ""
			continue
		}
		if isAutoNodeDomainComment(pendingComment) {
			pendingComment = ""
			continue
		}
		if r, ok := parseSingleRoutingLine(line, pendingComment, idx+1, true); ok {
			rules = append(rules, r)
		}
		pendingComment = ""
	}
	return rules, fallback
}

func parseSingleRoutingLine(line, comment string, idx int, enabled bool) (model.RoutingRule, bool) {
	parts := strings.SplitN(line, "->", 2)
	if len(parts) != 2 {
		return model.RoutingRule{}, false
	}
	lhs := strings.TrimSpace(parts[0])
	outbound := strings.TrimSpace(parts[1])
	if lhs == "" || outbound == "" {
		return model.RoutingRule{}, false
	}

	var conds []model.RoutingCondition
	allParsed := true
	for _, seg := range splitTopLevelAnd(lhs) {
		m := condItemRe.FindStringSubmatch(strings.TrimSpace(seg))
		if len(m) == 4 {
			conds = append(conds, model.RoutingCondition{
				Negate: m[1] == "!",
				Field:  m[2],
				Value:  strings.TrimSpace(m[3]),
			})
		} else {
			allParsed = false
			break
		}
	}

	rule := model.RoutingRule{
		ID:       fmt.Sprintf("rt-imp-%d-%d", time.Now().UnixNano()%100000, idx),
		Enabled:  enabled,
		Name:     comment,
		Category: inferCategory(lhs, outbound),
		Outbound: outbound,
	}
	if allParsed && len(conds) > 0 {
		rule.Conditions = conds
	} else {
		rule.RawExpr = lhs
	}
	return rule, true
}

func inferCategory(lhs, outbound string) string {
	l := strings.ToLower(lhs + " " + outbound)
	switch {
	case outbound == "block":
		return "block"
	case strings.Contains(l, "openai") || strings.Contains(l, "claude") || strings.Contains(l, "gemini") || strings.Contains(l, "ai"):
		return "ai"
	case strings.Contains(l, "netflix") || strings.Contains(l, "youtube") || strings.Contains(l, "disney") || strings.Contains(l, "media") || strings.Contains(l, "stream"):
		return "media"
	case outbound == "must_direct" || strings.Contains(l, "pname"):
		return "system"
	case outbound == "direct":
		return "direct"
	default:
		return "custom"
	}
}

// ParseConfigDAE imports a standard config.dae text into an AppState without duplicating subscription nodes.
func ParseConfigDAE(cfg string, base model.AppState) (model.AppState, error) {
	errs := ValidateSyntax(cfg)
	if len(errs) > 0 {
		return base, fmt.Errorf("配置文件语法错误: %s", strings.Join(errs, "; "))
	}

	blocks := extractTopLevelBlocks(cfg)
	st := base.Clone()
	st.UpdatedAt = time.Now().Format(time.RFC3339)

	// Build alias -> existing active node lookup BEFORE mutating st.Nodes so subscription nodes & pins round-trip cleanly
	prevActive := EnabledNodes(base)
	prevAliases := nodeAliases(prevActive)
	aliasToSubNode := map[string]model.Node{}
	uriToSubNodes := map[string][]model.Node{}
	for i, n := range prevActive {
		if n.SubID != "" {
			aliasToSubNode[prevAliases[i]] = n
			uriToSubNodes[strings.TrimSpace(n.URI)] = append(uriToSubNodes[strings.TrimSpace(n.URI)], n)
		}
	}

	if gBody, ok := blocks["global"]; ok {
		parseGlobalBlock(gBody, &st.Global)
	}
	if subBody, ok := blocks["subscription"]; ok {
		subs := parseKeyValueBlock(subBody)
		if len(subs) > 0 {
			st.Subscriptions = nil
			for idx, kv := range subs {
				st.Subscriptions = append(st.Subscriptions, model.Subscription{
					ID:              fmt.Sprintf("sub-imp-%d", idx+1),
					Tag:             kv.Key,
					Name:            kv.CommentOrKey(),
					URL:             kv.Val,
					Format:          "auto",
					UserAgent:       "clash.meta",
					Enabled:         true,
					AutoUpdateHours: 6,
					UpdatedAt:       st.UpdatedAt,
				})
			}
		}
	}
	if nodeBody, ok := blocks["node"]; ok {
		kvs := parseKeyValueBlock(nodeBody)
		usedSubNodeIDs := map[string]bool{}
		var nextNodes []model.Node
		aliasToResolvedNode := map[string]model.Node{}

		for idx, kv := range kvs {
			canonicalURI := strings.TrimSpace(kv.Val)
			parsedNode, parsedOK := subparser.ParseNodeURI(kv.Val, "", "", idx+1)
			if parsedOK {
				canonicalURI = strings.TrimSpace(parsedNode.URI)
			}

			// 1. Check if this node entry matches an existing subscription node by rendered alias + URI
			if subN, ok := aliasToSubNode[kv.Key]; ok && !usedSubNodeIDs[subN.ID] && strings.TrimSpace(subN.URI) == canonicalURI {
				usedSubNodeIDs[subN.ID] = true
				subN.Enabled = true
				nextNodes = append(nextNodes, subN)
				aliasToResolvedNode[kv.Key] = subN
				continue
			}
			// 2. Or matches an existing subscription node by exact canonical URI
			if candidates := uriToSubNodes[canonicalURI]; len(candidates) > 0 {
				var matchedSub *model.Node
				for ci := range candidates {
					if !usedSubNodeIDs[candidates[ci].ID] {
						matchedSub = &candidates[ci]
						break
					}
				}
				if matchedSub != nil {
					usedSubNodeIDs[matchedSub.ID] = true
					cp := *matchedSub
					cp.Enabled = true
					nextNodes = append(nextNodes, cp)
					aliasToResolvedNode[kv.Key] = cp
					continue
				}
			}

			// 3. Otherwise import as a manual node (reusing existing manual node ID if tag+URI match)
			var manual model.Node
			if parsedOK {
				manual = parsedNode
				manual.Tag = kv.Key
				if kv.Comment != "" {
					manual.Name = kv.Comment
				}
			} else {
				manual = model.Node{
					ID:        fmt.Sprintf("node-imp-%d-%d", time.Now().UnixNano()%100000, idx+1),
					Tag:       kv.Key,
					Name:      kv.CommentOrKey(),
					Protocol:  inferProtocol(kv.Val),
					URI:       kv.Val,
					Region:    "OTHER",
					Enabled:   true,
					LatencyMs: -1,
				}
			}
			for _, old := range base.Nodes {
				if old.SubID == "" && old.Tag == manual.Tag && strings.TrimSpace(old.URI) == strings.TrimSpace(manual.URI) {
					manual.ID = old.ID
					manual.LatencyMs = old.LatencyMs
					manual.Alive = old.Alive
					manual.CheckedAt = old.CheckedAt
					break
				}
			}
			nextNodes = append(nextNodes, manual)
			aliasToResolvedNode[kv.Key] = manual
		}

		// Preserve disabled subscription nodes (or nodes from disabled subscriptions) without duplicating enabled ones
		disabledSubs := map[string]bool{}
		for _, s := range st.Subscriptions {
			if !s.Enabled {
				disabledSubs[s.ID] = true
			}
		}
		for _, old := range base.Nodes {
			if old.SubID != "" && !usedSubNodeIDs[old.ID] {
				cp := old
				if !disabledSubs[old.SubID] {
					// Was previously enabled in an enabled sub, but removed from raw config.dae -> mark disabled
					cp.Enabled = false
				}
				nextNodes = append(nextNodes, cp)
			}
		}
		st.Nodes = nextNodes

		// Parse group block in exact declaration order and bind single-node fixed pins back to PinnedNodeID
		if grpBody, ok := blocks["group"]; ok {
			orderedGroups := extractOrderedBlocks(grpBody)
			if len(orderedGroups) > 0 {
				var newGroups []model.Group
				for idx, gb := range orderedGroups {
					grp := model.Group{
						ID:      fmt.Sprintf("grp-imp-%d", idx+1),
						Name:    gb.Name,
						Enabled: true,
						Policy:  "min_moving_avg",
						Mode:    "raw",
					}
					for _, prevG := range base.Groups {
						if prevG.Name == gb.Name {
							grp.ID = prevG.ID
							grp.Description = prevG.Description
							break
						}
					}
					var filters []string
					for _, line := range strings.Split(gb.Body, "\n") {
						t := strings.TrimSpace(stripInlineComment(line))
						if strings.HasPrefix(t, "filter:") {
							filters = append(filters, t)
						} else if strings.HasPrefix(t, "policy:") {
							p := strings.TrimSpace(strings.TrimPrefix(t, "policy:"))
							if m := fixedIndexRe.FindStringSubmatch(p); len(m) == 2 {
								grp.Policy = "fixed"
								grp.FixedIndex, _ = strconv.Atoi(m[1])
							} else if strings.HasPrefix(p, "fixed(") {
								grp.Policy = "fixed"
							} else {
								grp.Policy = p
							}
						}
					}
					grp.RawFilterDSL = strings.Join(filters, "\n")
					if grp.Policy == "fixed" && grp.FixedIndex == 0 && len(filters) == 1 {
						if m := singleNameRe.FindStringSubmatch(filters[0]); len(m) == 2 {
							if targetNode, ok := aliasToResolvedNode[m[1]]; ok {
								grp.PinnedNodeID = targetNode.ID
								grp.PinnedNodeTag = targetNode.Tag
								grp.Mode = "smart"
							}
						}
					}
					newGroups = append(newGroups, grp)
				}
				st.Groups = newGroups
			}
		}
	} else if grpBody, ok := blocks["group"]; ok {
		orderedGroups := extractOrderedBlocks(grpBody)
		if len(orderedGroups) > 0 {
			var newGroups []model.Group
			for idx, gb := range orderedGroups {
				grp := model.Group{
					ID:      fmt.Sprintf("grp-imp-%d", idx+1),
					Name:    gb.Name,
					Enabled: true,
					Policy:  "min_moving_avg",
					Mode:    "raw",
				}
				for _, prevG := range base.Groups {
					if prevG.Name == gb.Name {
						grp.ID = prevG.ID
						grp.Description = prevG.Description
						break
					}
				}
				var filters []string
				for _, line := range strings.Split(gb.Body, "\n") {
					t := strings.TrimSpace(stripInlineComment(line))
					if strings.HasPrefix(t, "filter:") {
						filters = append(filters, t)
					} else if strings.HasPrefix(t, "policy:") {
						p := strings.TrimSpace(strings.TrimPrefix(t, "policy:"))
						if m := fixedIndexRe.FindStringSubmatch(p); len(m) == 2 {
							grp.Policy = "fixed"
							grp.FixedIndex, _ = strconv.Atoi(m[1])
						} else if strings.HasPrefix(p, "fixed(") {
							grp.Policy = "fixed"
						} else {
							grp.Policy = p
						}
					}
				}
				grp.RawFilterDSL = strings.Join(filters, "\n")
				newGroups = append(newGroups, grp)
			}
			st.Groups = newGroups
		}
	}

	isDirectPlaceholder := strings.Contains(cfg, "[DirectWithoutNodes]") && base.DirectWithoutNodes && len(st.Nodes) == 0

	if dnsBody, ok := blocks["dns"]; ok {
		if isDirectPlaceholder {
			var candDNS model.DNSConfig
			parseDNSBlock(dnsBody, &candDNS)
			isUntouchedPlaceholderDNS := len(candDNS.Upstreams) == 1 &&
				(candDNS.Upstreams[0].Tag == "alidns" || candDNS.Upstreams[0].Tag == "cndns") &&
				len(candDNS.RequestRules) == 0 &&
				len(candDNS.ResponseRules) == 0 &&
				candDNS.RequestFallback == candDNS.Upstreams[0].Tag
			if isUntouchedPlaceholderDNS {
				st.DNS.Bind = candDNS.Bind
			} else {
				st.DNS = candDNS
			}
		} else {
			parseDNSBlock(dnsBody, &st.DNS)
		}
	}
	if rtBody, ok := blocks["routing"]; ok {
		rules, fb := ParseRoutingDSL(rtBody)
		if !(isDirectPlaceholder && len(rules) == 0 && fb == "direct") {
			st.Routing.Mode = "visual"
			st.Routing.Rules = rules
			st.Routing.Fallback = fb
			st.Routing.RawDSL = strings.TrimSpace(rtBody)
		}
	}

	return st, nil
}

type kvPair struct {
	Key     string
	Val     string
	Comment string
}

func (k kvPair) CommentOrKey() string {
	if k.Comment != "" {
		return k.Comment
	}
	return k.Key
}

func parseKeyValueBlock(body string) []kvPair {
	var out []kvPair
	var comment string
	for _, raw := range strings.Split(body, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "#") {
			comment = strings.TrimSpace(strings.TrimPrefix(t, "#"))
			continue
		}
		t = stripInlineComment(t)
		idx := strings.Index(t, ":")
		if idx <= 0 {
			val := strings.Trim(t, "'\"")
			if strings.Contains(val, "://") {
				out = append(out, kvPair{Key: fmt.Sprintf("item_%d", len(out)+1), Val: val, Comment: comment})
			}
			comment = ""
			continue
		}
		key := strings.TrimSpace(t[:idx])
		val := strings.Trim(strings.TrimSpace(t[idx+1:]), "'\"")
		out = append(out, kvPair{Key: key, Val: val, Comment: comment})
		comment = ""
	}
	return out
}

func parseGlobalBlock(body string, g *model.GlobalConfig) {
	for _, kv := range parseKeyValueBlock(body) {
		switch kv.Key {
		case "wan_interface":
			g.WanInterface = kv.Val
		case "lan_interface":
			parts := strings.Split(kv.Val, ",")
			var lans []string
			for _, p := range parts {
				if strings.TrimSpace(p) != "" {
					lans = append(lans, strings.TrimSpace(p))
				}
			}
			g.LanInterfaces = lans
		case "dial_mode":
			g.DialMode = kv.Val
		case "log_level":
			g.LogLevel = kv.Val
		case "tproxy_port":
			if v, err := strconv.Atoi(kv.Val); err == nil {
				g.TproxyPort = v
			}
		case "tproxy_port_protect":
			g.TproxyPortProtect = kv.Val == "true"
		case "allow_insecure":
			g.AllowInsecure = kv.Val == "true"
		case "auto_config_kernel_parameter":
			g.AutoConfigKernelParameter = kv.Val == "true"
		case "check_interval":
			g.CheckInterval = kv.Val
		case "check_tolerance":
			g.CheckTolerance = kv.Val
		case "tcp_check_url":
			g.TcpCheckUrl = kv.Val
		case "tcp_check_http_method":
			g.TcpCheckHttpMethod = kv.Val
		case "udp_check_dns":
			g.UdpCheckDns = kv.Val
		case "sniffing_timeout":
			g.SniffingTimeout = kv.Val
		case "tls_implementation":
			g.TlsImplementation = kv.Val
		case "utls_imitate":
			g.UtlsImitate = kv.Val
		case "mptcp":
			g.Mptcp = kv.Val == "true"
		case "disable_waiting_network":
			g.DisableWaitingNetwork = kv.Val == "true"
		case "so_mark_from_dae":
			if v, err := strconv.Atoi(kv.Val); err == nil {
				g.SoMarkFromDae = v
			}
		}
	}
}

func extractTopLevelBlocks(src string) map[string]string {
	res := map[string]string{}
	for _, b := range extractOrderedBlocks(src) {
		res[b.Name] = b.Body
	}
	return res
}

func extractOrderedBlocks(src string) []namedBlock {
	var out []namedBlock
	n := len(src)
	i := 0
	for i < n {
		for i < n && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
			i++
		}
		if i < n && src[i] == '#' {
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		startName := i
		for i < n && ((src[i] >= 'a' && src[i] <= 'z') || (src[i] >= 'A' && src[i] <= 'Z') || (src[i] >= '0' && src[i] <= '9') || src[i] == '_' || src[i] == '-') {
			i++
		}
		name := strings.TrimSpace(src[startName:i])
		for i < n && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
			i++
		}
		if name == "" || i >= n || src[i] != '{' {
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		i++ // skip '{'
		bodyStart := i
		depth := 1
		quote := byte(0)
		for i < n && depth > 0 {
			ch := src[i]
			if ch == '\\' && quote != 0 && i+1 < n {
				i += 2
				continue
			}
			if quote == 0 && ch == '#' {
				for i < n && src[i] != '\n' {
					i++
				}
				continue
			}
			if ch == '\'' || ch == '"' {
				if quote == 0 {
					quote = ch
				} else if quote == ch {
					quote = 0
				}
			} else if quote == 0 {
				if ch == '{' {
					depth++
				} else if ch == '}' {
					depth--
					if depth == 0 {
						out = append(out, namedBlock{Name: name, Body: src[bodyStart:i]})
						i++
						break
					}
				}
			}
			i++
		}
	}
	return out
}

func splitTopLevelAnd(expr string) []string {
	var parts []string
	depth := 0
	quote := byte(0)
	last := 0
	for i := 0; i < len(expr); i++ {
		ch := expr[i]
		if ch == '\\' && quote != 0 && i+1 < nLen(expr) {
			i++
			continue
		}
		if ch == '\'' || ch == '"' {
			if quote == 0 {
				quote = ch
			} else if quote == ch {
				quote = 0
			}
		} else if quote == 0 {
			if ch == '(' {
				depth++
			} else if ch == ')' && depth > 0 {
				depth--
			} else if depth == 0 && i+1 < len(expr) && expr[i:i+2] == "&&" {
				parts = append(parts, expr[last:i])
				i++
				last = i + 1
			}
		}
	}
	parts = append(parts, expr[last:])
	return parts
}

func nLen(s string) int { return len(s) }

func stripInlineComment(s string) string {
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && quote != 0 && i+1 < len(s) {
			i++
			continue
		}
		if ch == '\'' || ch == '"' {
			if quote == 0 {
				quote = ch
			} else if quote == ch {
				quote = 0
			}
		} else if quote == 0 && ch == '#' {
			return strings.TrimSpace(s[:i])
		}
	}
	return strings.TrimSpace(s)
}

func inferProtocol(uri string) string {
	if idx := strings.Index(uri, "://"); idx > 0 {
		p := strings.ToLower(uri[:idx])
		if p == "hy2" {
			return "hysteria2"
		}
		return p
	}
	return "unknown"
}

// ParseDNSDSL parses a raw DNS block body (or wrapped `dns { ... }`) into a structured DNSConfig.
func ParseDNSDSL(raw string) model.DNSConfig {
	var cfg model.DNSConfig
	parseDNSBlock(unwrapBlockBody(raw, "dns"), &cfg)
	return cfg
}

func parseDNSBlock(body string, dns *model.DNSConfig) {
	sub := extractTopLevelBlocks(body)
	upBody, hasUp := sub["upstream"]
	rtBody, hasRt := sub["routing"]
	if !hasUp || !hasRt {
		dns.Mode = "dsl"
		dns.RawDSL = strings.TrimSpace(body)
		return
	}

	dns.Mode = "visual"
	dns.Bind = ""
	dns.IpVersionPrefer = 0
	braceDepth := 0
	quote := byte(0)
	for _, raw := range strings.Split(body, "\n") {
		clean := stripInlineComment(raw)
		if braceDepth == 0 {
			t := strings.TrimSpace(clean)
			if strings.HasPrefix(t, "bind:") {
				dns.Bind = strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "bind:")), "'\"")
			} else if strings.HasPrefix(t, "ipversion_prefer:") {
				v := strings.TrimSpace(strings.TrimPrefix(t, "ipversion_prefer:"))
				if n, err := strconv.Atoi(v); err == nil {
					dns.IpVersionPrefer = n
				}
			}
		}
		for i := 0; i < len(clean); i++ {
			ch := clean[i]
			if ch == '\\' && quote != 0 && i+1 < len(clean) {
				i++
				continue
			}
			if ch == '\'' || ch == '"' {
				if quote == 0 {
					quote = ch
				} else if quote == ch {
					quote = 0
				}
			} else if quote == 0 {
				if ch == '{' {
					braceDepth++
				} else if ch == '}' && braceDepth > 0 {
					braceDepth--
				}
			}
		}
	}

	var upstreams []model.DNSUpstream
	for _, kv := range parseKeyValueBlock(upBody) {
		upstreams = append(upstreams, model.DNSUpstream{
			Tag:  kv.Key,
			URL:  kv.Val,
			Note: kv.Comment,
		})
	}
	dns.Upstreams = upstreams

	rtSub := extractTopLevelBlocks(rtBody)
	if reqBody, ok := rtSub["request"]; ok {
		rules, fb := parseDNSRuleLines(reqBody, "req")
		dns.RequestRules = rules
		if fb != "" {
			dns.RequestFallback = fb
		}
	}
	if respBody, ok := rtSub["response"]; ok {
		rules, fb := parseDNSRuleLines(respBody, "resp")
		dns.ResponseRules = rules
		if fb != "" {
			dns.ResponseFallback = fb
		}
	} else {
		dns.ResponseRules = nil
		dns.ResponseFallback = "accept"
	}
}

func parseDNSRuleLines(body, prefix string) ([]model.DNSRule, string) {
	var out []model.DNSRule
	fallback := ""
	comment := ""
	for idx, raw := range strings.Split(body, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "#") {
			comment = strings.TrimSpace(strings.TrimPrefix(t, "#"))
			continue
		}
		t = stripInlineComment(t)
		if strings.HasPrefix(t, "fallback:") {
			fallback = strings.TrimSpace(strings.TrimPrefix(t, "fallback:"))
			comment = ""
			continue
		}
		if isAutoNodeDomainComment(comment) {
			comment = ""
			continue
		}
		if parts := strings.SplitN(t, "->", 2); len(parts) == 2 {
			out = append(out, model.DNSRule{
				ID:      fmt.Sprintf("dns-%s-%d", prefix, idx+1),
				Enabled: true,
				Expr:    strings.TrimSpace(parts[0]),
				Target:  strings.TrimSpace(parts[1]),
				Comment: comment,
			})
		}
		comment = ""
	}
	return out, fallback
}
