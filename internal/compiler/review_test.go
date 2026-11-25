package compiler

import (
	"strings"
	"testing"

	"daed-next/internal/model"
)

func TestRoundTripDoesNotDuplicateSubNodesOrAutoRules(t *testing.T) {
	base := model.DefaultState()
	base.Subscriptions = []model.Subscription{
		{
			ID:      "sub-airport-1",
			Tag:     "airport_main",
			Name:    "主力订阅",
			URL:     "https://sub.example.com/s",
			Enabled: true,
		},
	}
	base.Nodes = []model.Node{
		{
			ID:        "nd-1",
			Tag:       "hk_01",
			Name:      "香港 01",
			Protocol:  "trojan",
			Address:   "entry.airport.example.com",
			Port:      443,
			URI:       "trojan://pass@entry.airport.example.com:443?sni=entry.airport.example.com#%E9%A6%99%E6%B8%AF%2001",
			SubID:     "sub-airport-1",
			SubTag:    "airport_main",
			Region:    "HK",
			Enabled:   true,
			LatencyMs: 32,
		},
		{
			ID:        "nd-2",
			Tag:       "jp_disabled",
			Name:      "日本 停用",
			Protocol:  "trojan",
			Address:   "jp.airport.example.com",
			Port:      443,
			URI:       "trojan://pass@jp.airport.example.com:443?sni=jp.airport.example.com#JP",
			SubID:     "sub-airport-1",
			SubTag:    "airport_main",
			Region:    "JP",
			Enabled:   false,
			LatencyMs: 58,
		},
		{
			ID:       "nd-manual",
			Tag:      "self_vless",
			Name:     "自建节点",
			Protocol: "vless",
			Address:  "self.example.org",
			Port:     443,
			URI:      "vless://11111111-1111-1111-1111-111111111111@self.example.org:443?security=tls#Self",
			Enabled:  true,
		},
	}
	base.Groups = []model.Group{
		{
			ID:           "g-1",
			Name:         "proxy",
			Enabled:      true,
			Policy:       "fixed",
			PinnedNodeID: "nd-1",
			PinnedNodeTag: "hk_01",
		},
		{
			ID:      "g-2",
			Name:    "alpha_group",
			Enabled: true,
			Policy:  "min_moving_avg",
			Mode:    "smart",
		},
		{
			ID:      "g-3",
			Name:    "zeta_group",
			Enabled: true,
			Policy:  "fixed",
			FixedIndex: 1,
			Mode:    "smart",
		},
	}

	origRuleCount := len(base.Routing.Rules)
	origDNSRuleCount := len(base.DNS.RequestRules)

	cur := base
	for round := 0; round < 3; round++ {
		cfg := RenderConfig(cur)
		if !strings.Contains(cfg, "entry.airport.example.com") {
			t.Fatalf("round %d: expected anti-deadlock domain rule in config", round)
		}
		parsed, err := ParseConfigDAE(cfg, cur)
		if err != nil {
			t.Fatalf("round %d ParseConfigDAE failed: %v", round, err)
		}
		if len(parsed.Nodes) != 3 {
			t.Fatalf("round %d: expected 3 nodes (no subscription node duplication), got %d", round, len(parsed.Nodes))
		}
		if len(parsed.Routing.Rules) != origRuleCount {
			t.Fatalf("round %d: auto-injected routing rules leaked into state: got %d want %d", round, len(parsed.Routing.Rules), origRuleCount)
		}
		if len(parsed.DNS.RequestRules) != origDNSRuleCount {
			t.Fatalf("round %d: auto-injected DNS rules leaked into state: got %d want %d", round, len(parsed.DNS.RequestRules), origDNSRuleCount)
		}
		if len(parsed.Groups) != 3 || parsed.Groups[0].Name != "proxy" || parsed.Groups[1].Name != "alpha_group" || parsed.Groups[2].Name != "zeta_group" {
			t.Fatalf("round %d: group order not preserved: %+v", round, parsed.Groups)
		}
		if parsed.Groups[0].PinnedNodeID != "nd-1" {
			t.Fatalf("round %d: pinned node ID lost after round-trip: %+v", round, parsed.Groups[0])
		}
		if parsed.Groups[2].FixedIndex != 1 {
			t.Fatalf("round %d: fixed(1) index lost after round-trip: %+v", round, parsed.Groups[2])
		}
		cur = parsed
	}
}

func TestDisableWaitingNetworkRespected(t *testing.T) {
	st := model.DefaultState()
	st.Global.DisableWaitingNetwork = false
	cfgFalse := RenderConfig(st)
	if !strings.Contains(cfgFalse, "disable_waiting_network: false") {
		t.Fatalf("expected disable_waiting_network: false, got:\n%s", cfgFalse)
	}

	st.Global.DisableWaitingNetwork = true
	cfgTrue := RenderConfig(st)
	if !strings.Contains(cfgTrue, "disable_waiting_network: true") {
		t.Fatalf("expected disable_waiting_network: true, got:\n%s", cfgTrue)
	}
}

func TestQuotedStringsInSyntaxAndRoutingDSL(t *testing.T) {
	dsl := `
# Double and single quoted expressions
domain(keyword: "foo#bar && baz") && dip('224.0.0.0/3') -> proxy
fallback: direct
`
	rules, fb := ParseRoutingDSL(dsl)
	if fb != "direct" || len(rules) != 1 || len(rules[0].Conditions) != 2 {
		t.Fatalf("unexpected ParseRoutingDSL output: fb=%q rules=%+v", fb, rules)
	}
	cfg := "global {\n  log_level: info\n}\nrouting {\n" + dsl + "\n}\n"
	if errs := ValidateSyntax(cfg); len(errs) != 0 {
		t.Fatalf("expected valid syntax with double quotes containing '#', got: %v", errs)
	}
}

func TestValidateOutboundsCatchesDeletedGroupReference(t *testing.T) {
	st := model.DefaultState()
	// Remove "streaming" group while routing rules still reference "streaming"
	var kept []model.Group
	for _, g := range st.Groups {
		if g.Name != "streaming" {
			kept = append(kept, g)
		}
	}
	st.Groups = kept
	err := ValidateOutbounds(st)
	if err == nil || !strings.Contains(err.Error(), "streaming") {
		t.Fatalf("expected ValidateOutbounds to report dangling 'streaming' outbound, got: %v", err)
	}
}

func TestParseRoutingDSLIgnoresDNSBlockWhenFullConfigPasted(t *testing.T) {
	fullCfg := `
dns {
    routing {
        request {
            qname(geosite:cn) -> cndns
            fallback: fallbackdns
        }
    }
}
routing {
    dip(223.5.5.5) -> direct
    fallback: proxy
}
`
	rules, fb := ParseRoutingDSL(fullCfg)
	if fb != "proxy" || len(rules) != 1 || rules[0].Outbound != "direct" {
		t.Fatalf("ParseRoutingDSL leaked dns.routing rules: fb=%q rules=%+v", fb, rules)
	}
}

func TestValidateSyntaxRequiresRealTopLevelRoutingBlock(t *testing.T) {
	// Mentioning "routing" only inside a comment or inside dns.routing must fail!
	onlyComment := "# routing rules placeholder\nglobal {\n  log_level: info\n}\n"
	if errs := ValidateSyntax(onlyComment); len(errs) == 0 {
		t.Fatal("expected ValidateSyntax to reject config with '# routing' comment but no top-level routing {}")
	}

	onlyDNSRouting := "dns {\n  routing {\n    request {\n      fallback: alidns\n    }\n  }\n}\n"
	if errs := ValidateSyntax(onlyDNSRouting); len(errs) == 0 {
		t.Fatal("expected ValidateSyntax to reject config with only dns.routing {} and no top-level routing {}")
	}
}

func TestParseDNSDSLOrderIndependenceAndGroupMetadataPreservation(t *testing.T) {
	// bind and ipversion_prefer placed AFTER upstream {} must still be parsed!
	rawDNS := `
upstream {
  cndns: 'https://223.5.5.5/dns-query'
  fallbackdns: 'https://1.1.1.1/dns-query'
}
bind: '127.0.0.1:15353'
ipversion_prefer: 4
routing {
  request {
    qname(geosite:cn) -> cndns
    fallback: fallbackdns
  }
}
`
	dnsCfg := ParseDNSDSL(rawDNS)
	if dnsCfg.Bind != "127.0.0.1:15353" || dnsCfg.IpVersionPrefer != 4 {
		t.Fatalf("expected bind and ipversion_prefer parsed after upstream block, got: %+v", dnsCfg)
	}
	if len(dnsCfg.Upstreams) != 2 || dnsCfg.RequestFallback != "fallbackdns" || len(dnsCfg.RequestRules) != 1 {
		t.Fatalf("unexpected ParseDNSDSL output: %+v", dnsCfg)
	}

	// Group ID and Description must be preserved even when node {} block is absent (subscription-only config)
	base := model.DefaultState()
	base.Nodes = nil
	base.Groups = []model.Group{
		{
			ID:          "grp-custom-id",
			Name:        "proxy",
			Description: "保留自定义描述",
			Enabled:     true,
			Policy:      "min_moving_avg",
			Mode:        "smart",
		},
	}
	cfgNoNodeBlock := `
global {
  log_level: warn
}
subscription {
  airport_main: 'https://sub.example.com/s'
}
group {
  proxy {
    filter: subtag(airport_main)
    policy: min_moving_avg
  }
}
routing {
  fallback: proxy
}
`
	parsed, err := ParseConfigDAE(cfgNoNodeBlock, base)
	if err != nil {
		t.Fatalf("ParseConfigDAE failed: %v", err)
	}
	if len(parsed.Groups) != 1 || parsed.Groups[0].ID != "grp-custom-id" || parsed.Groups[0].Description != "保留自定义描述" {
		t.Fatalf("expected group ID and Description preserved when node {} block is absent, got: %+v", parsed.Groups)
	}
}



