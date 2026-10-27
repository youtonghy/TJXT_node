package core

import (
	"encoding/json"
	"testing"

	"github.com/xtls/xray-core/proxy/freedom"
	"github.com/youtonghy/TJXT_node/api/panel"
)

func TestCustomOutboundRoutes(t *testing.T) {
	valid := `{"tag":"proxy-out","protocol":"freedom"}`
	invalidJSON := `{"tag":`
	rejected := `{"tag":"bad-out","protocol":"no-such-protocol"}`
	info := nodeWithEgress("in", "")
	info.Common.Routes = []panel.Route{
		{Id: 1, Rule: json.RawMessage(`{"domain":["example.com"],"outbound":{"tag":"proxy-out","protocol":"freedom"}}`)},
		{Id: 2, Rule: json.RawMessage(`{"ip":["1.1.1.1/32"],"outboundTag":"proxy-out"}`)},
		{Id: 3, Rule: json.RawMessage(invalidJSON)},
		{Id: 4, Rule: json.RawMessage(rejected)},
		{Id: 5, Rule: json.RawMessage(`{"domain":["bad.example"],"outboundTag":"bad-out"}`)},
		{Id: 6, Action: "route", Match: []string{"legacy.example"}, ActionValue: &valid},
	}
	_, outbounds, router, err := GetCustomConfig([]*panel.NodeInfo{info})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	count := 0
	for _, o := range outbounds {
		switch o.Tag {
		case "proxy-out":
			count++
		case "bad-out":
			t.Fatal("rejected outbound must not be added")
		}
	}
	if count != 1 {
		t.Fatalf("expected the shared outbound once, got %d", count)
	}
	targets := map[string]int{}
	for _, rule := range router.GetRule() {
		targets[rule.GetTag()]++
	}
	if targets["proxy-out"] != 3 {
		t.Fatalf("expected 3 rules to proxy-out, got %v", targets)
	}
	if targets["bad-out"] != 0 {
		t.Fatalf("no rule may point at a rejected outbound, got %v", targets)
	}
}

func TestStackedXrayJsonOutboundRulesOrder(t *testing.T) {
	info := nodeWithEgress("node-a", "")
	info.Common.Routes = []panel.Route{
		{Id: 10, Rule: json.RawMessage(`{"domain":["full:blocked.test"],"outboundTag":"block"}`)},
		{Id: 11, Rule: json.RawMessage(`{"domain":["full:direct.test"],"outboundTag":"direct"}`)},
		{Id: 12, Rule: json.RawMessage(`{"tag":"fallback-out","protocol":"freedom"}`)},
	}
	_, outbounds, router, err := GetCustomConfig([]*panel.NodeInfo{info})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !hasOutboundWithTag(outbounds, "fallback-out") {
		t.Fatal("expected fallback-out outbound to be registered")
	}
	rules := router.GetRule()
	// Rule 0 is the built-in UDP/53 -> dns_out rule; rules 1..3 come from the stacked routes.
	if len(rules) != 4 {
		t.Fatalf("expected 4 router rules, got %d", len(rules))
	}
	wantTags := []string{"dns_out", "block", DefaultOutboundTag, "fallback-out"}
	for i, want := range wantTags {
		if got := rules[i].GetTag(); got != want {
			t.Fatalf("rule[%d] tag = %q, want %q", i, got, want)
		}
	}
}

func TestCustomFreedomOutboundBlocksPrivateEgress(t *testing.T) {
	custom := `{"tag":"custom-direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow"}]}}`
	for _, tc := range []struct {
		name       string
		baseConfig string
		block      bool
	}{
		{name: "default", baseConfig: "", block: true},
		{name: "allowed", baseConfig: `{"allow_private_egress":true}`, block: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := nodeWithEgress("in", tc.baseConfig)
			info.Common.Routes = []panel.Route{{Id: 1, Rule: json.RawMessage(custom)}}
			_, outbounds, _, err := GetCustomConfig([]*panel.NodeInfo{info})
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			c := freedomConfig(t, outbounds, "custom-direct")
			if c == nil {
				t.Fatal("custom outbound missing")
			}
			rules := c.GetFinalRules()
			blocked := len(rules) == 2 && rules[0].GetAction() == freedom.RuleAction_Block && len(rules[0].GetIp()) > 0
			if tc.block != blocked {
				t.Fatalf("block=%v, rules=%v", tc.block, rules)
			}
			if !tc.block && (len(rules) != 1 || rules[0].GetAction() != freedom.RuleAction_Allow) {
				t.Fatalf("panel rules must stay untouched when allowed: %v", rules)
			}
		})
	}
}

func TestCustomFreedomOutboundAllowsPrivateWhenNodeAllows(t *testing.T) {
	custom := `{"tag":"custom-redirect","protocol":"freedom","settings":{"redirect":"10.0.0.2:80"}}`
	info := nodeWithEgress("in", `{"allow_private_egress":true}`)
	info.Common.Routes = []panel.Route{{Id: 1, Rule: json.RawMessage(custom)}}
	_, outbounds, _, err := GetCustomConfig([]*panel.NodeInfo{info})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	c := freedomConfig(t, outbounds, "custom-redirect")
	if c == nil || !allowsAll(c) {
		t.Fatalf("expected an explicit allow-all rule, got %v", c.GetFinalRules())
	}
}

func TestCompositeOutboundsWithoutRulesCatchAllTraffic(t *testing.T) {
	info := nodeWithEgress("node-a", "")
	info.Common.Routes = []panel.Route{
		{Id: 1, Rule: json.RawMessage(`{"outbounds":[{"tag":"custom-out","protocol":"freedom"},{"tag":"spare-out","protocol":"freedom"}]}`)},
		{Id: 2, Rule: json.RawMessage(`{"tag":"later-out","protocol":"freedom"}`)},
	}
	_, _, router, err := GetCustomConfig([]*panel.NodeInfo{info})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got []string
	for _, rule := range router.GetRule() {
		got = append(got, rule.GetTag())
	}
	want := []string{"dns_out", "custom-out", "later-out"}
	if len(got) != len(want) {
		t.Fatalf("rule tags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rule tags = %v, want %v", got, want)
		}
	}
}

func TestCompositeOutboundReferencedByRuleIsNotCatchAll(t *testing.T) {
	info := nodeWithEgress("node-a", "")
	info.Common.Routes = []panel.Route{
		{Id: 1, Rule: json.RawMessage(`{"outbounds":[{"tag":"custom-out","protocol":"freedom"}],"rules":[{"domain":["full:split.test"],"outboundTag":"custom-out"}]}`)},
	}
	_, _, router, err := GetCustomConfig([]*panel.NodeInfo{info})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	rules := router.GetRule()
	if len(rules) != 2 || rules[1].GetTag() != "custom-out" {
		t.Fatalf("expected only the explicit split rule, got %d rules", len(rules))
	}
}
