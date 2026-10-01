package core

import (
	"encoding/json"
	"testing"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/freedom"
	"github.com/youtonghy/TJXT_node/api/panel"
)

func nodeWithEgress(tag string, baseConfig string) *panel.NodeInfo {
	common := &panel.CommonNode{}
	if baseConfig != "" {
		common.BaseConfig = &panel.BaseConfig{}
		if err := json.Unmarshal([]byte(baseConfig), common.BaseConfig); err != nil {
			panic(err)
		}
	}
	return &panel.NodeInfo{Tag: tag, Common: common}
}

func freedomConfig(t *testing.T, outbounds []*core.OutboundHandlerConfig, tag string) *freedom.Config {
	t.Helper()
	for _, o := range outbounds {
		if o.Tag != tag {
			continue
		}
		msg, err := o.ProxySettings.GetInstance()
		if err != nil {
			t.Fatalf("decode %s: %v", tag, err)
		}
		return msg.(*freedom.Config)
	}
	return nil
}

func blocksPrivate(c *freedom.Config) bool {
	rules := c.GetFinalRules()
	return len(rules) == 1 && rules[0].GetAction() == freedom.RuleAction_Block && len(rules[0].GetIp()) > 0
}

func allowsAll(c *freedom.Config) bool {
	rules := c.GetFinalRules()
	return len(rules) == 1 && rules[0].GetAction() == freedom.RuleAction_Allow && len(rules[0].GetIp()) == 0
}

func TestDefaultOutboundPrivateEgress(t *testing.T) {
	cases := []struct {
		name       string
		baseConfig string
		allow      bool
	}{
		{name: "absent base_config", baseConfig: "", allow: false},
		{name: "absent field", baseConfig: `{"push_interval":60}`, allow: false},
		{name: "false", baseConfig: `{"allow_private_egress":false}`, allow: false},
		{name: "true", baseConfig: `{"allow_private_egress":true}`, allow: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, outbounds, _, err := GetCustomConfig([]*panel.NodeInfo{nodeWithEgress("a", tc.baseConfig)})
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			def := freedomConfig(t, outbounds, DefaultOutboundTag)
			if def == nil {
				t.Fatal("no default outbound")
			}
			if tc.allow && !allowsAll(def) {
				t.Fatalf("expected allow-all, got %v", def.GetFinalRules())
			}
			if !tc.allow && !blocksPrivate(def) {
				t.Fatalf("expected private block, got %v", def.GetFinalRules())
			}
			if freedomConfig(t, outbounds, allowPrivateOutboundTag) != nil {
				t.Fatal("no extra outbound expected for a single node")
			}
		})
	}
}

func TestMixedNodesGetPerNodeOutbound(t *testing.T) {
	infos := []*panel.NodeInfo{
		nodeWithEgress("deny", `{"allow_private_egress":false}`),
		nodeWithEgress("allow", `{"allow_private_egress":true}`),
	}
	_, outbounds, _, err := GetCustomConfig(infos)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !blocksPrivate(freedomConfig(t, outbounds, DefaultOutboundTag)) {
		t.Fatal("the shared default outbound must block private egress")
	}
	allow := freedomConfig(t, outbounds, allowPrivateOutboundTag)
	if allow == nil || !allowsAll(allow) {
		t.Fatal("the allowing node needs its own allow-all outbound")
	}
	allowAll, tags := privateEgressPolicy(infos)
	if allowAll || len(tags) != 1 || tags[0] != "allow" {
		t.Fatalf("unexpected policy %v %v", allowAll, tags)
	}
}
