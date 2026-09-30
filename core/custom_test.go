package core

import (
	"testing"

	panel "github.com/youtonghy/TJXT_node/api/v2board"
)

func TestCustomOutboundRoutes(t *testing.T) {
	valid := `{"tag":"proxy-out","protocol":"freedom"}`
	invalidJSON := `{"tag":`
	rejected := `{"tag":"bad-out","protocol":"no-such-protocol"}`
	info := nodeWithEgress("in", "")
	info.Common.Routes = []panel.Route{
		{Id: 1, Action: "route", Match: []string{"example.com"}, ActionValue: &valid},
		{Id: 2, Action: "route_ip", Match: []string{"1.1.1.1/32"}, ActionValue: &valid},
		{Id: 3, Action: "route", Match: []string{"a.test"}, ActionValue: &invalidJSON},
		{Id: 4, Action: "default_out", ActionValue: &rejected},
		{Id: 5, Action: "route"},
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
	if targets["proxy-out"] != 2 {
		t.Fatalf("expected 2 rules to proxy-out, got %v", targets)
	}
	if targets["bad-out"] != 0 {
		t.Fatalf("no rule may point at a rejected outbound, got %v", targets)
	}
}
