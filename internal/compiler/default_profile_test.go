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
	for _, want := range []string{
		"fallback: proxy",
		"-> streaming",
		"tcp://1.1.1.1:53",
		"udp://223.5.5.5:53",
		"qname(full:example.com) -> alidns",
		"domain(full:example.com) -> must_direct",
		"domain(geosite:geolocation-!cn) -> proxy",
		"tls_implementation: utls",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("missing %s in:\n%s", want, cfg)
		}
	}
	for _, unwanted := range []string{"dnsmasq", "geosite:google-cn", "dip(223.5.5.5"} {
		if strings.Contains(cfg, unwanted) {
			t.Fatalf("unexpected %s in default profile:\n%s", unwanted, cfg)
		}
	}
	if len(st.Groups) != 2 || len(st.Routing.Rules) == 0 {
		t.Fatal("render discarded stored template")
	}

	// Editing global settings in raw config.dae while DirectWithoutNodes has 0 nodes must preserve the stored template
	emptyBase := model.ProductionDefaults()
	editedEmpty := strings.Replace(RenderConfig(emptyBase), "log_level: warn", "log_level: info", 1)
	reimported, err := ParseConfigDAE(editedEmpty, emptyBase)
	if err != nil {
		t.Fatalf("unexpected ParseConfigDAE error on empty-node config: %v", err)
	}
	if reimported.Global.LogLevel != "info" {
		t.Fatalf("expected global log_level updated to info, got %q", reimported.Global.LogLevel)
	}
	if len(reimported.Groups) != 2 || len(reimported.Routing.Rules) != len(emptyBase.Routing.Rules) || reimported.Routing.Fallback != "proxy" || len(reimported.DNS.Upstreams) != 2 {
		t.Fatalf("raw import during DirectWithoutNodes wiped stored routing/DNS template: groups=%d rules=%d fb=%q dns=%d",
			len(reimported.Groups), len(reimported.Routing.Rules), reimported.Routing.Fallback, len(reimported.DNS.Upstreams))
	}
}
