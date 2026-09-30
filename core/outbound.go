package core

import (
	"fmt"

	"encoding/json"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
)

// DefaultOutboundTag is the fallback freedom outbound; allowPrivateOutboundTag
// is the freedom outbound used by nodes that allow private egress while others
// in the same core do not.
const (
	DefaultOutboundTag      = "Default"
	allowPrivateOutboundTag = "Default-allow-private"
)

// privateEgressRanges are blocked unless a node allows private egress. It is
// Xray's own private list: the pinned core applies it by default only to some
// inbound protocols (not http, mixed, tuic, anytls, tunnel, tun), so it is set
// explicitly. It covers RFC 1918, CGNAT, loopback (127/8, ::1), link-local and
// cloud metadata (169.254/16, fe80::/10), ULA (fc00::/7) and reserved ranges.
var privateEgressRanges = []string{
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/3",
	"::/127",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
}

// buildFreedomOutbound builds a freedom outbound that either allows every
// destination or blocks the private ranges (domains are resolved first, so a
// name pointing at a private address is blocked too).
func buildFreedomOutbound(tag string, allowPrivate bool) (*core.OutboundHandlerConfig, error) {
	outboundDetourConfig := &conf.OutboundDetourConfig{}
	outboundDetourConfig.Protocol = "freedom"
	outboundDetourConfig.Tag = tag
	//sendthrough := "origin"
	//outboundDetourConfig.SendThrough = &sendthrough

	rule := &conf.FreedomFinalRuleConfig{Action: "allow"}
	if !allowPrivate {
		ips := conf.StringList(privateEgressRanges)
		rule = &conf.FreedomFinalRuleConfig{Action: "block", IP: &ips}
	}
	proxySetting := &conf.FreedomConfig{
		DomainStrategy: "UseIPv4v6",
		FinalRules:     []*conf.FreedomFinalRuleConfig{rule},
	}
	var setting json.RawMessage
	setting, err := json.Marshal(proxySetting)
	if err != nil {
		return nil, fmt.Errorf("marshal proxy config error: %s", err)
	}
	outboundDetourConfig.Settings = &setting
	return outboundDetourConfig.Build()
}

// build block outbund
func buildBlockOutbound() (*core.OutboundHandlerConfig, error) {
	outboundDetourConfig := &conf.OutboundDetourConfig{}
	outboundDetourConfig.Protocol = "blackhole"
	outboundDetourConfig.Tag = "block"
	return outboundDetourConfig.Build()
}

// build dns outbound
func buildDnsOutbound() (*core.OutboundHandlerConfig, error) {
	outboundDetourConfig := &conf.OutboundDetourConfig{}
	outboundDetourConfig.Protocol = "dns"
	outboundDetourConfig.Tag = "dns_out"
	return outboundDetourConfig.Build()
}
