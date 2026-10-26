package core

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	xcore "github.com/xtls/xray-core/core"
	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/common/format"
)

func sharedTestNode(id, shared int, routes ...panel.Route) *panel.NodeInfo {
	tag := "[https://panel.example]-vless:" + string(rune('0'+id))
	inbound := tag
	if shared > 0 {
		inbound = "[https://panel.example]-vless:" + string(rune('0'+shared))
	}
	return &panel.NodeInfo{
		Id: id, Type: "vless", Tag: tag, InboundTag: inbound, SharedInbound: shared,
		Common: &panel.CommonNode{Routes: routes, BaseConfig: &panel.BaseConfig{}},
	}
}

func TestSharedInboundNodesRouteByTheirUsers(t *testing.T) {
	route := panel.Route{Id: 1, Rule: json.RawMessage(`{"domain":["example.com"],"outboundTag":"block"}`)}
	entry, line, plain := sharedTestNode(1, 0, route), sharedTestNode(2, 1, route), sharedTestNode(3, 0, route)
	bindings := nodeBindings([]*panel.NodeInfo{entry, line, plain})
	if !bindings[entry.Tag].byUser || !bindings[line.Tag].byUser || bindings[plain.Tag].byUser {
		t.Fatalf("bindings = %+v", bindings)
	}
	for _, tc := range []struct {
		node   *panel.NodeInfo
		match  string
		absent string
	}{{line, `"user"`, `"inboundTag"`}, {entry, `"user"`, `"inboundTag"`}, {plain, `"inboundTag"`, `"user"`}} {
		block, err := buildBlockOutbound()
		if err != nil {
			t.Fatal(err)
		}
		_, _, rules := applyNodeRoutes(nil, []*xcore.OutboundHandlerConfig{block}, nil, bindings[tc.node.Tag], tc.node.Common.Routes, true)
		if len(rules) != 1 || !strings.Contains(string(rules[0]), tc.match) || strings.Contains(string(rules[0]), tc.absent) {
			t.Fatalf("%s: rules = %s", tc.node.Tag, rules)
		}
	}
	// The user pattern selects exactly one node's users.
	re := regexp.MustCompile(strings.TrimPrefix(userPattern(line.Tag), "regexp:"))
	if !re.MatchString(format.UserTag(line.Tag, "u-1")) || re.MatchString(format.UserTag(entry.Tag, "u-1")) {
		t.Fatal("user pattern does not separate the nodes of a shared inbound")
	}
	// The whole config builds with user-matched rules.
	if _, _, _, err := GetCustomConfig([]*panel.NodeInfo{entry, line, plain}); err != nil {
		t.Fatalf("build: %v", err)
	}
}

func TestNodeTagOfEmail(t *testing.T) {
	email := format.UserTag("[https://panel.example]-vless:2", "6f1c-uuid")
	if format.NodeTagOf(email) != "[https://panel.example]-vless:2" || format.NodeTagOf("plain@example.com") != "" {
		t.Fatal("node tag not read from the email")
	}
}
