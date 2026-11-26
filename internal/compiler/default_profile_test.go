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
	for _, want := range []string{"fallback: proxy", "tcp://1.1.1.1:53", "qname(full:example.com) -> alidns", "domain(full:example.com) -> must_direct"} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("missing %s", want)
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

// Keep the shipped template aligned with the confirmed factory settings.
func TestDefaultDNSAndRoutingDoNotRegress(t *testing.T) {
	st := model.ProductionDefaults()
	if st.Global.TlsImplementation != "utls" || st.Global.UtlsImitate == "" {
		t.Fatal("factory profile must provide a Reality-compatible TLS fingerprint")
	}
	if st.DNS.Mode != "visual" || st.DNS.Bind != "127.0.0.1:15353" || len(st.DNS.Upstreams) != 2 || len(st.DNS.RequestRules) != 3 || len(st.Routing.Rules) != 9 {
		t.Fatal("factory profile must match the approved router DNS and nine routing rules")
	}
	if st.DNS.RequestFallback != "cfdns" || st.Routing.Fallback != "proxy" {
		t.Fatal("incorrect factory fallback")
	}
	st.Nodes = []model.Node{{ID: "test", Tag: "test", Enabled: true, URI: "trojan://test@example.com:443#test", Address: "example.com", Protocol: "trojan", Port: 443}}
	cfg := RenderConfig(st)
	for _, unwanted := range []string{"amdc.m.taobao.com", "ximalaya", "xmcdn.com"} {
		if strings.Contains(cfg, unwanted) {
			t.Fatalf("removed factory exception returned: %s", unwanted)
		}
	}
	for _, rule := range st.Routing.Rules {
		if rule.Outbound == "block" && rule.Enabled {
			t.Fatal("factory routing must not enable blocking by default")
		}
	}
}

func TestAllNodesGroupStillEmitsFilter(t *testing.T) {
	nodes := []model.Node{{ID: "one", Tag: "one", Enabled: true}, {ID: "two", Tag: "two", Enabled: true}}
	for _, count := range []int{1, 2} {
		active := nodes[:count]
		g := model.Group{Name: "proxy", Mode: "smart", Policy: "min_moving_avg"}
		lines := renderGroupFilter(g, active, nodeAliases(active))
		if len(lines) != 1 || !strings.HasPrefix(lines[0], "filter: name(") {
			t.Fatalf("%d nodes: group has no explicit filter: %v", count, lines)
		}
	}
}

func TestRealityFingerprintDefault(t *testing.T) {
	raw := "vless://test@example.com:443?security=reality&pbk=test#node"
	if !strings.Contains(runtimeNodeURI(raw), "fp=chrome") {
		t.Fatal("Reality link without fp must receive a usable default")
	}
	explicit := raw + "&fp=firefox"
	explicit = strings.Replace(explicit, "#node", "", 1)
	if runtimeNodeURI(explicit) != explicit {
		t.Fatal("explicit fingerprint must be preserved")
	}
	if runtimeNodeURI("trojan://test@example.com:443") != "trojan://test@example.com:443" {
		t.Fatal("unrelated links changed")
	}
}

func TestIncompleteRealityCannotApply(t *testing.T) {
	st := model.ProductionDefaults()
	st.Nodes = []model.Node{{ID: "one", Tag: "one", Enabled: true, URI: "vless://test@example.com:443?security=reality&sni=example.com"}}
	if err := ValidateOutbounds(st); err == nil || !strings.Contains(err.Error(), "pbk") {
		t.Fatal("missing Reality public key must fail before applying")
	}
}
