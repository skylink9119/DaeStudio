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
	empty := RenderConfig(st)
	if !strings.Contains(empty, "fallback: direct") || strings.Contains(empty, "-> streaming") {
		t.Fatal("empty installation must render a usable direct configuration")
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
	for _, want := range []string{"fallback: proxy", "-> streaming", "https://1.1.1.1/dns-query", "qname(full:example.com) -> cndns", "domain(full:example.com) -> must_direct"} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if len(st.Groups) != 2 || len(st.Routing.Rules) == 0 {
		t.Fatal("render discarded stored template")
	}
}
