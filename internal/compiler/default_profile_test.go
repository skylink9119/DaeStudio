package compiler

import (
	"daed-next/internal/model"
	"strings"
	"testing"
)

func TestProductionDefaultsBeforeAndAfterNodeImport(t *testing.T) {
	st := model.ProductionDefaults()
	if len(st.Nodes) != 0 || len(st.Subscriptions) != 0 || len(st.Snapshots) != 0 {
		t.Fatal("default profile contains user data")
	}
	if err := ValidateOutbounds(st); err != nil {
		t.Fatal(err)
	}
	for _, rule := range st.Routing.Rules {
		for _, cond := range rule.Conditions {
			if strings.Contains(cond.Value, "taobao") || strings.Contains(cond.Value, "ximalaya") {
				t.Fatal("removed exception remains in default routing")
			}
		}
	}
	if strings.Contains(st.DNS.RawDSL, "taobao") || strings.Contains(st.DNS.RawDSL, "ximalaya") {
		t.Fatal("removed exception remains in default DNS")
	}
	empty := RenderConfig(st)
	if !strings.Contains(empty, "fallback: direct") || strings.Contains(empty, "-> streaming") {
		t.Fatal("empty installation must render a usable direct configuration")
	}
	for _, want := range []string{"bind: '127.0.0.1:15353'", "optimistic_cache: true", "optimistic_cache_ttl: 60", "max_cache_size: 8192"} {
		if !strings.Contains(empty, want) {
			t.Fatalf("empty configuration missing %s", want)
		}
	}
	st.Nodes = []model.Node{{ID: "new-node", Tag: "new-node", Enabled: true, SubTag: "new-subscription", Region: "DE", URI: "trojan://test@example.com:443#test", Address: "example.com", Protocol: "trojan", Port: 443}}
	for _, g := range st.Groups {
		if len(MatchNodesForGroup(g, EnabledNodes(st))) != 1 {
			t.Fatalf("group %s excludes imported node", g.Name)
		}
	}
	if err := ValidateOutbounds(st); err != nil {
		t.Fatal(err)
	}
	cfg := RenderConfig(st)
	for _, want := range []string{"optimistic_cache: true", "max_cache_size: 8192", "geosite:apple@cn", "fallback: proxy", "-> streaming", "https://1.1.1.1/dns-query", "qname(full:example.com) -> cndns", "domain(full:example.com) -> must_direct"} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if len(st.Groups) != 2 || len(st.Routing.Rules) == 0 {
		t.Fatal("render discarded stored template")
	}
}
