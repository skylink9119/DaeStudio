package compiler

import (
	"strings"
	"testing"

	"daed-next/internal/model"
)

func TestSwitchUsesExactNodeAndSource(t *testing.T) {
	state := model.AppState{
		Subscriptions: []model.Subscription{{ID: "one", Tag: "same", Enabled: true}, {ID: "two", Tag: "same", Enabled: true}},
		Nodes: []model.Node{
			{ID: "first", Tag: "same", Enabled: true, SubID: "one", URI: "trojan://first@one.example:443"},
			{ID: "second", Tag: "same", Enabled: true, SubID: "two", URI: "trojan://second@two.example:443"},
		},
		Groups:  []model.Group{{Name: "proxy", Enabled: true, Policy: "fixed", PinnedNodeID: "second", PinnedNodeTag: "same", FixedIndex: 4}},
		Routing: model.RoutingConfig{Fallback: "proxy"},
	}
	if err := ValidateOutbounds(state); err != nil {
		t.Fatal(err)
	}
	cfg := RenderConfig(state)
	for _, line := range []string{"same_2: 'trojan://second", "filter: name(same_2)", "policy: fixed(0)"} {
		if !strings.Contains(cfg, line) {
			t.Errorf("missing %q in rendered config", line)
		}
	}
	state.Groups[0].PinnedNodeID = ""
	if err := ValidateOutbounds(state); err == nil {
		t.Fatal("accepted ambiguous legacy pin")
	}
	state.Groups[0].PinnedNodeTag = ""
	state.Groups[0].Policy = "min"
	state.Groups[0].Filter.SubIDs = []string{"two"}
	if cfg = RenderConfig(state); !strings.Contains(cfg, "filter: name(same_2)") {
		t.Fatal("route source did not isolate same-tag subscriptions")
	}
	state.Subscriptions[1].Enabled = false
	if err := ValidateOutbounds(state); err == nil {
		t.Fatal("accepted empty routed group")
	}
	if cfg = RenderConfig(state); !strings.Contains(cfg, "filter: name(regex: '^$')") {
		t.Fatal("an empty group must not silently match all enabled nodes")
	}
}
