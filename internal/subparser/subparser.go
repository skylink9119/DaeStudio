package subparser

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"daed-next/internal/model"
)

var nodeIDCounter atomic.Uint64

// SubMeta holds traffic quota metadata parsed from Subscription-Userinfo headers.
type SubMeta struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   int64
}

// ParseUserInfoHeader parses standard airport Subscription-Userinfo HTTP headers.
func ParseUserInfoHeader(hdr string) SubMeta {
	var m SubMeta
	for _, part := range strings.Split(hdr, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		val, err := strconv.ParseInt(strings.TrimSpace(kv[1]), 10, 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(kv[0])) {
		case "upload":
			m.Upload = val
		case "download":
			m.Download = val
		case "total":
			m.Total = val
		case "expire":
			m.Expire = val
		}
	}
	return m
}

// ParseSubscriptionContent parses raw subscription text (URI list, Base64, Clash YAML, or Sing-box JSON) into standard dae Node objects.
func ParseSubscriptionContent(raw string, subID, subTag, excludeRegex string) ([]model.Node, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, "empty", fmt.Errorf("订阅内容为空")
	}

	excludeRe, err := compileExcludeRegex(excludeRegex)
	if err != nil {
		return nil, "invalid_regex", err
	}

	// 1. Detect Sing-box JSON (`{"outbounds": [...]}`)
	if strings.HasPrefix(trimmed, "{") {
		nodes, err := parseSingboxJSON(trimmed, subID, subTag)
		if err == nil && len(nodes) > 0 {
			return finalizeFilteredNodes(nodes, excludeRe, "singbox")
		}
	}

	// 2. Detect Clash / Mihomo YAML (`proxies:`)
	if strings.Contains(trimmed, "proxies:") {
		nodes := parseClashYAML(trimmed, subID, subTag)
		if len(nodes) > 0 {
			return finalizeFilteredNodes(nodes, excludeRe, "clash")
		}
	}

	// 3. Try Base64 decoding if not directly starting with protocol schemes
	detectedFormat := "uri"
	decoded := trimmed
	if !strings.Contains(trimmed, "://") {
		if b, err := decodeBase64Flexible(trimmed); err == nil && strings.Contains(b, "://") {
			decoded = b
			detectedFormat = "base64"
		}
	}

	// 4. Parse URI lines
	var nodes []model.Node
	for idx, line := range strings.Split(decoded, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if n, ok := ParseNodeURI(line, subID, subTag, idx+1); ok {
			nodes = append(nodes, n)
		}
	}
	if len(nodes) == 0 {
		return nil, detectedFormat, fmt.Errorf("未识别到有效的代理节点（支持 Base64 / 节点链接 / Clash YAML / Sing-box JSON）")
	}
	return finalizeFilteredNodes(nodes, excludeRe, detectedFormat)
}

func compileExcludeRegex(expr string) (*regexp.Regexp, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}
	// Strip empty alternation branches (e.g. "剩余流量|到期|" -> "剩余流量|到期") so a trailing '|' never matches everything.
	rawParts := strings.Split(expr, "|")
	cleaned := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		if s := strings.TrimSpace(p); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 {
		return nil, nil
	}
	re, err := regexp.Compile(strings.Join(cleaned, "|"))
	if err != nil {
		return nil, fmt.Errorf("排除节点名称正则语法无效: %w", err)
	}
	return re, nil
}

func finalizeFilteredNodes(nodes []model.Node, excludeRe *regexp.Regexp, format string) ([]model.Node, string, error) {
	filtered := filterNodes(nodes, excludeRe)
	if len(nodes) > 0 && len(filtered) == 0 {
		return nil, format, fmt.Errorf("识别到 %d 个节点，但已全部被排除规则过滤，请检查排除关键词设置", len(nodes))
	}
	return filtered, format, nil
}

func filterNodes(nodes []model.Node, excludeRe *regexp.Regexp) []model.Node {
	if excludeRe == nil {
		return nodes
	}
	var out []model.Node
	for _, n := range nodes {
		if !excludeRe.MatchString(n.Name) {
			out = append(out, n)
		}
	}
	return out
}

// ParseNodeURI parses a single vless/vmess/trojan/ss/hysteria2/tuic/socks5 URI into a Node model.
func ParseNodeURI(uriStr, subID, subTag string, seq int) (model.Node, bool) {
	uriStr = strings.TrimSpace(uriStr)
	idx := strings.Index(uriStr, "://")
	if idx <= 0 {
		return model.Node{}, false
	}
	scheme := strings.ToLower(uriStr[:idx])
	if scheme == "hy2" {
		scheme = "hysteria2"
	}

	now := time.Now().UnixMilli()
	node := model.Node{
		ID:        fmt.Sprintf("node-%d-%d-%d", now, nodeIDCounter.Add(1), seq),
		Protocol:  scheme,
		URI:       uriStr,
		SubID:     subID,
		SubTag:    subTag,
		Enabled:   true,
		LatencyMs: -1,
	}

	if scheme == "vmess" {
		payload := strings.TrimPrefix(uriStr, uriStr[:idx+3])
		if rawJSON, err := decodeBase64Flexible(payload); err == nil {
			var vm map[string]any
			if json.Unmarshal([]byte(rawJSON), &vm) == nil {
				node.Name = strVal(vm["ps"], fmt.Sprintf("VMess-%d", seq))
				node.Address = strVal(vm["add"], "")
				node.Port = intVal(vm["port"], 443)
				node.Tag = buildTag(node.Name, seq)
				node.Region = DetectRegion(node.Name)
				return node, node.Address != ""
			}
		}
	}

	u, err := url.Parse(uriStr)
	if err != nil {
		return model.Node{}, false
	}
	name := u.Fragment
	if decodedName, err := url.QueryUnescape(name); err == nil && decodedName != "" {
		name = decodedName
	}
	if name == "" {
		if rm := u.Query().Get("remarks"); rm != "" {
			name = rm
		}
	}
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())

	// Handle Shadowrocket / legacy base64-encoded authority (e.g. ss://BASE64#name or vless://BASE64?remarks=...)
	if port == 0 && u.User == nil && host != "" {
		if dec, err := decodeBase64Flexible(host); err == nil {
			hp := dec
			userPart := ""
			if atIdx := strings.LastIndex(dec, "@"); atIdx > 0 {
				userPart = dec[:atIdx]
				hp = dec[atIdx+1:]
			}
			if h, pStr, splitErr := splitHostPortFlexible(hp); splitErr == nil {
				host = h
				port, _ = strconv.Atoi(pStr)
				// Normalize Shadowrocket vless://BASE64?tls=1&pbk=... into standard dae Reality/TLS vless:// URI
				if scheme == "vless" && userPart != "" {
					uuid := userPart
					if colonIdx := strings.LastIndex(userPart, ":"); colonIdx >= 0 {
						uuid = userPart[colonIdx+1:]
					}
					node.URI = normalizeShadowrocketVLESS(uuid, host, pStr, u.Query(), name)
				}
			}
		}
	}
	if port == 0 {
		port = 443
	}
	if name == "" {
		name = fmt.Sprintf("%s-%s:%d", strings.ToUpper(scheme), host, port)
	}
	// Normalize Shadowrocket query params (peer -> sni, obfs=grpc -> type=grpc&serviceName=..., obfs=websocket -> type=ws)
	if node.URI == uriStr && (scheme == "trojan" || scheme == "vless" || scheme == "ss" || scheme == "hysteria2") {
		node.URI = normalizeShadowrocketQueryURI(u, scheme, host, port, name)
	}
	node.Name = name
	node.Address = host
	node.Port = port
	node.Tag = buildTag(name, seq)
	node.Region = DetectRegion(name)
	return node, host != ""
}

// DetectRegion classifies node region from its display name.
func DetectRegion(name string) string {
	u := strings.ToUpper(name)
	switch {
	case strings.Contains(u, "HK") || strings.Contains(name, "香港") || strings.Contains(name, "港"):
		return "HK"
	case strings.Contains(u, "TW") || strings.Contains(name, "台湾") || strings.Contains(name, "台北") || strings.Contains(name, "新北"):
		return "TW"
	case strings.Contains(u, "JP") || strings.Contains(name, "日本") || strings.Contains(name, "东京") || strings.Contains(name, "大阪"):
		return "JP"
	case strings.Contains(u, "SG") || strings.Contains(name, "新加坡") || strings.Contains(name, "狮城"):
		return "SG"
	case strings.Contains(u, "US") || strings.Contains(name, "美国") || strings.Contains(name, "硅谷") || strings.Contains(name, "洛杉矶") || strings.Contains(name, "西雅图"):
		return "US"
	case strings.Contains(u, "KR") || strings.Contains(name, "韩国") || strings.Contains(name, "首尔"):
		return "KR"
	case strings.Contains(u, "DE") || strings.Contains(name, "德国") || strings.Contains(name, "法兰克福"):
		return "DE"
	case strings.Contains(u, "UK") || strings.Contains(u, "GB") || strings.Contains(name, "英国") || strings.Contains(name, "伦敦"):
		return "UK"
	default:
		return "OTHER"
	}
}

// parseSingboxJSON converts Sing-box outbounds JSON into standard dae Node items.
func parseSingboxJSON(raw, subID, subTag string) ([]model.Node, error) {
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, err
	}
	var out []model.Node
	for i, ob := range doc.Outbounds {
		tp := strings.ToLower(strVal(ob["type"], ""))
		server := strVal(ob["server"], "")
		port := intVal(ob["server_port"], 443)
		tag := strVal(ob["tag"], fmt.Sprintf("sb-%d", i+1))
		if server == "" {
			continue
		}
		host := server
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			host = "[" + host + "]"
		}
		var uri string
		switch tp {
		case "shadowsocks":
			method := strVal(ob["method"], "aes-256-gcm")
			pass := strVal(ob["password"], "")
			cred := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(method + ":" + pass))
			uri = fmt.Sprintf("ss://%s@%s:%d#%s", cred, host, port, url.PathEscape(tag))
		case "trojan":
			pass := strVal(ob["password"], "")
			sni := nestedStr(ob, "tls", "server_name")
			uri = fmt.Sprintf("trojan://%s@%s:%d?sni=%s#%s", url.QueryEscape(pass), host, port, url.QueryEscape(sni), url.PathEscape(tag))
		case "vless":
			uuid := strVal(ob["uuid"], "")
			sni := nestedStr(ob, "tls", "server_name")
			flow := strVal(ob["flow"], "")
			uri = fmt.Sprintf("vless://%s@%s:%d?encryption=none&security=tls&sni=%s&flow=%s#%s", uuid, host, port, url.QueryEscape(sni), url.QueryEscape(flow), url.PathEscape(tag))
		case "hysteria2":
			pass := strVal(ob["password"], "")
			sni := nestedStr(ob, "tls", "server_name")
			uri = fmt.Sprintf("hysteria2://%s@%s:%d?sni=%s#%s", url.QueryEscape(pass), host, port, url.QueryEscape(sni), url.PathEscape(tag))
		}
		if uri != "" {
			if n, ok := ParseNodeURI(uri, subID, subTag, i+1); ok {
				out = append(out, n)
			}
		}
	}
	return out, nil
}

// parseClashYAML extracts flow-style `{name: ..., type: ..., server: ...}` or block-style proxies from Clash/Mihomo YAML without external deps.
func parseClashYAML(raw, subID, subTag string) []model.Node {
	var items []map[string]string
	lines := strings.Split(raw, "\n")
	inProxies := false
	var cur map[string]string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(trimmed, "-") {
			if strings.HasPrefix(trimmed, "proxies:") {
				inProxies = true
				continue
			}
			if inProxies {
				break
			}
		}
		if !inProxies {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			if len(cur) > 0 {
				items = append(items, cur)
				cur = nil
			}
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if strings.HasPrefix(rest, "{") && strings.HasSuffix(rest, "}") {
				items = append(items, parseFlowMap(rest[1:len(rest)-1]))
				continue
			}
			cur = map[string]string{}
			if idx := strings.Index(rest, ":"); idx > 0 {
				cur[strings.TrimSpace(rest[:idx])] = unquoteYAML(rest[idx+1:])
			}
		} else if cur != nil {
			if idx := strings.Index(trimmed, ":"); idx > 0 {
				k := strings.TrimSpace(trimmed[:idx])
				v := unquoteYAML(trimmed[idx+1:])
				if v != "" {
					cur[k] = v
				}
			}
		}
	}
	if len(cur) > 0 {
		items = append(items, cur)
	}

	var out []model.Node
	for i, p := range items {
		uri := clashProxyToURI(p)
		if uri != "" {
			if n, ok := ParseNodeURI(uri, subID, subTag, i+1); ok {
				out = append(out, n)
			}
		}
	}
	return out
}

func clashProxyToURI(p map[string]string) string {
	tp := strings.ToLower(p["type"])
	name := p["name"]
	server := p["server"]
	port := p["port"]
	if server == "" || port == "" {
		return ""
	}
	if name == "" {
		name = tp + "-" + server
	}
	host := server
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	sni := p["servername"]
	if sni == "" {
		sni = p["sni"]
	}
	switch tp {
	case "ss":
		cipher := p["cipher"]
		pass := p["password"]
		cred := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(cipher + ":" + pass))
		return fmt.Sprintf("ss://%s@%s:%s#%s", cred, host, port, url.PathEscape(name))
	case "trojan":
		q := url.Values{}
		if sni != "" {
			q.Set("sni", sni)
		}
		if p["skip-cert-verify"] == "true" {
			q.Set("allowInsecure", "1")
		}
		if p["network"] != "" {
			q.Set("type", p["network"])
		}
		if p["path"] != "" {
			q.Set("path", p["path"])
		}
		if p["Host"] != "" {
			q.Set("host", p["Host"])
		}
		if p["grpc-service-name"] != "" {
			q.Set("serviceName", p["grpc-service-name"])
		}
		return fmt.Sprintf("trojan://%s@%s:%s?%s#%s", url.QueryEscape(p["password"]), host, port, q.Encode(), url.PathEscape(name))
	case "vless":
		q := url.Values{}
		q.Set("encryption", "none")
		if p["pbk"] != "" || p["public-key"] != "" {
			q.Set("security", "reality")
			pbk := p["pbk"]
			if pbk == "" {
				pbk = p["public-key"]
			}
			q.Set("pbk", pbk)
			if p["short-id"] != "" {
				q.Set("sid", p["short-id"])
			}
		} else if p["tls"] == "true" {
			q.Set("security", "tls")
		}
		if sni != "" {
			q.Set("sni", sni)
		}
		if p["flow"] != "" {
			q.Set("flow", p["flow"])
		}
		if p["client-fingerprint"] != "" {
			q.Set("fp", p["client-fingerprint"])
		}
		if p["network"] != "" {
			q.Set("type", p["network"])
		}
		if p["path"] != "" {
			q.Set("path", p["path"])
		}
		if p["Host"] != "" {
			q.Set("host", p["Host"])
		}
		if p["grpc-service-name"] != "" {
			q.Set("serviceName", p["grpc-service-name"])
		}
		return fmt.Sprintf("vless://%s@%s:%s?%s#%s", p["uuid"], host, port, q.Encode(), url.PathEscape(name))
	case "hysteria2":
		q := url.Values{}
		if sni != "" {
			q.Set("sni", sni)
		}
		if p["skip-cert-verify"] == "true" {
			q.Set("insecure", "1")
		}
		if p["obfs"] != "" {
			q.Set("obfs", p["obfs"])
			if p["obfs-password"] != "" {
				q.Set("obfs-password", p["obfs-password"])
			}
		}
		return fmt.Sprintf("hysteria2://%s@%s:%s?%s#%s", url.QueryEscape(p["password"]), host, port, q.Encode(), url.PathEscape(name))
	case "vmess":
		netType := defaultStr(p["network"], "tcp")
		vmHost := p["Host"]
		if vmHost == "" {
			vmHost = sni
		}
		vmPath := p["path"]
		vmHeaderType := "none"
		if netType == "grpc" {
			vmHeaderType = "gun"
			if vmPath == "" {
				vmPath = p["grpc-service-name"]
			}
		}
		vm := map[string]any{
			"v":    "2",
			"ps":   name,
			"add":  server,
			"port": port,
			"id":   p["uuid"],
			"aid":  "0",
			"net":  netType,
			"type": vmHeaderType,
			"host": vmHost,
			"path": vmPath,
			"tls":  mapBoolTLS(p["tls"]),
			"sni":  sni,
		}
		b, _ := json.Marshal(vm)
		return "vmess://" + base64.StdEncoding.EncodeToString(b)
	case "tuic":
		q := url.Values{}
		if sni != "" {
			q.Set("sni", sni)
		}
		q.Set("congestion_control", defaultStr(p["congestion-controller"], "bbr"))
		return fmt.Sprintf("tuic://%s:%s@%s:%s?%s#%s", url.QueryEscape(p["uuid"]), url.QueryEscape(p["password"]), host, port, q.Encode(), url.PathEscape(name))
	}
	return ""
}

func parseFlowMap(inner string) map[string]string {
	res := map[string]string{}
	depth := 0
	inQuote := byte(0)
	last := 0
	var parts []string
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			inQuote = c
		} else if c == '{' || c == '[' {
			depth++
		} else if c == '}' || c == ']' {
			if depth > 0 {
				depth--
			}
		} else if c == ',' && depth == 0 {
			parts = append(parts, inner[last:i])
			last = i + 1
		}
	}
	parts = append(parts, inner[last:])
	for _, seg := range parts {
		idx := strings.Index(seg, ":")
		if idx <= 0 {
			continue
		}
		k := strings.TrimSpace(seg[:idx])
		v := strings.TrimSpace(seg[idx+1:])
		if strings.HasPrefix(v, "{") && strings.HasSuffix(v, "}") {
			for nk, nv := range parseFlowMap(v[1 : len(v)-1]) {
				res[nk] = nv
			}
		} else {
			res[k] = unquoteYAML(v)
		}
	}
	return res
}

func unquoteYAML(s string) string {
	s = strings.TrimSpace(s)
	return strings.Trim(s, "'\"")
}

func decodeBase64Flexible(s string) (string, error) {
	clean := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		if b, err := enc.DecodeString(clean); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("invalid base64")
}

func buildTag(name string, seq int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteRune('_')
		}
	}
	res := strings.Trim(b.String(), "_")
	if res == "" {
		return fmt.Sprintf("node_%d", seq)
	}
	if res[0] >= '0' && res[0] <= '9' {
		res = "n_" + res
	}
	return fmt.Sprintf("%s_%d", res, seq)
}

func strVal(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func intVal(v any, def int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		if n, err := strconv.Atoi(x); err == nil {
			return n
		}
	}
	return def
}

func nestedStr(m map[string]any, k1, k2 string) string {
	if sub, ok := m[k1].(map[string]any); ok {
		return strVal(sub[k2], "")
	}
	return ""
}

func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func mapBoolTLS(s string) string {
	if s == "true" {
		return "tls"
	}
	return ""
}

func splitHostPortFlexible(hp string) (string, string, error) {
	hp = strings.TrimSpace(hp)
	if h, p, err := net.SplitHostPort(hp); err == nil {
		return strings.Trim(h, "[]"), p, nil
	}
	if colon := strings.LastIndex(hp, ":"); colon > 0 {
		return strings.Trim(hp[:colon], "[]"), hp[colon+1:], nil
	}
	return "", "", fmt.Errorf("invalid host:port")
}

func normalizeShadowrocketVLESS(uuid, host, port string, origQ url.Values, name string) string {
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("type", "tcp")
	if origQ.Get("pbk") != "" {
		q.Set("security", "reality")
		q.Set("pbk", origQ.Get("pbk"))
		if origQ.Get("sid") != "" {
			q.Set("sid", origQ.Get("sid"))
		}
	} else if origQ.Get("tls") == "1" {
		q.Set("security", "tls")
	}
	sni := origQ.Get("peer")
	if sni == "" {
		sni = origQ.Get("sni")
	}
	if sni != "" {
		q.Set("sni", sni)
	}
	if origQ.Get("xtls") == "2" {
		q.Set("flow", "xtls-rprx-vision")
	} else if origQ.Get("flow") != "" {
		q.Set("flow", origQ.Get("flow"))
	}
	if origQ.Get("fp") != "" {
		q.Set("fp", origQ.Get("fp"))
	}
	hostBracketed := host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		hostBracketed = "[" + host + "]"
	}
	frag := ""
	if name != "" {
		frag = "#" + url.PathEscape(name)
	}
	return fmt.Sprintf("vless://%s@%s:%s?%s%s", uuid, hostBracketed, port, q.Encode(), frag)
}

func normalizeShadowrocketQueryURI(u *url.URL, scheme, host string, port int, name string) string {
	origQ := u.Query()
	hasSRParams := origQ.Get("peer") != "" || origQ.Get("obfs") != "" || origQ.Get("obfsParam") != "" || origQ.Get("tfo") != ""
	if !hasSRParams {
		return u.String()
	}

	q := url.Values{}
	for k, vals := range origQ {
		switch k {
		case "peer", "obfs", "obfsParam", "path", "tfo", "remarks":
			continue
		default:
			for _, v := range vals {
				q.Add(k, v)
			}
		}
	}

	sni := origQ.Get("sni")
	if sni == "" {
		sni = origQ.Get("peer")
	}
	if sni != "" {
		q.Set("sni", sni)
	}

	obfs := strings.ToLower(origQ.Get("obfs"))
	switch obfs {
	case "grpc":
		q.Set("type", "grpc")
		if svc := origQ.Get("path"); svc != "" {
			q.Set("serviceName", svc)
		}
		if auth := origQ.Get("obfsParam"); auth != "" {
			q.Set("authority", auth)
		}
	case "websocket", "ws":
		q.Set("type", "ws")
		if p := origQ.Get("path"); p != "" {
			q.Set("path", p)
		}
		if h := origQ.Get("obfsParam"); h != "" {
			q.Set("host", h)
		}
	default:
		if p := origQ.Get("path"); p != "" {
			q.Set("path", p)
		}
	}

	hostBracketed := host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		hostBracketed = "[" + host + "]"
	}
	userInfo := ""
	if u.User != nil {
		userInfo = u.User.String() + "@"
	}
	frag := ""
	if name != "" {
		frag = "#" + url.PathEscape(name)
	}
	qStr := q.Encode()
	if qStr != "" {
		return fmt.Sprintf("%s://%s%s:%d?%s%s", scheme, userInfo, hostBracketed, port, qStr, frag)
	}
	return fmt.Sprintf("%s://%s%s:%d%s", scheme, userInfo, hostBracketed, port, frag)
}



