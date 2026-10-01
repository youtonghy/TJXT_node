package core

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/xtls/xray-core/app/dns"
	"github.com/xtls/xray-core/app/router"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	coreConf "github.com/xtls/xray-core/infra/conf"
	"github.com/youtonghy/TJXT_node/api/panel"
)

// hasPublicIPv6 checks if the machine has a public IPv6 address
func hasPublicIPv6() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		// Check if it's IPv6, not loopback, not link-local, not private/ULA
		if ip.To4() == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsPrivate() {
			return true
		}
	}
	return false
}

func hasOutboundWithTag(list []*core.OutboundHandlerConfig, tag string) bool {
	for _, o := range list {
		if o != nil && o.Tag == tag {
			return true
		}
	}
	return false
}

// privateEgressPolicy reports whether every node allows private egress and,
// if not, the inbound tags of the nodes that do.
func privateEgressPolicy(infos []*panel.NodeInfo) (allowAll bool, allowTags []string) {
	allowAll = len(infos) > 0
	for _, info := range infos {
		if info.AllowsPrivateEgress() {
			allowTags = append(allowTags, info.Tag)
		} else {
			allowAll = false
		}
	}
	if allowAll {
		return true, nil
	}
	return false, allowTags
}

func GetCustomConfig(infos []*panel.NodeInfo) (*dns.Config, []*core.OutboundHandlerConfig, *router.Config, error) {
	//dns
	queryStrategy := "UseIPv4v6"
	if !hasPublicIPv6() {
		queryStrategy = "UseIPv4"
	}
	coreDnsConfig := &coreConf.DNSConfig{
		Servers: []*coreConf.NameServerConfig{
			{
				Address: &coreConf.Address{
					Address: xnet.ParseAddress("localhost"),
				},
			},
		},
		QueryStrategy: queryStrategy,
	}
	//outbound
	// Private egress is per node, but nodes in one core share the fallback
	// outbound. It allows private destinations only if every node does; nodes
	// that allow them while others do not get their own outbound through a
	// lowest-priority routing rule added below.
	allowAll, allowTags := privateEgressPolicy(infos)
	defaultoutbound, err := buildFreedomOutbound(DefaultOutboundTag, allowAll)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build default outbound: %w", err)
	}
	coreOutboundConfig := append([]*core.OutboundHandlerConfig{}, defaultoutbound)
	block, err := buildBlockOutbound()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build block outbound: %w", err)
	}
	coreOutboundConfig = append(coreOutboundConfig, block)
	dns, err := buildDnsOutbound()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build dns outbound: %w", err)
	}
	coreOutboundConfig = append(coreOutboundConfig, dns)

	//route
	domainStrategy := "AsIs"
	dnsRule, _ := json.Marshal(map[string]interface{}{
		"port":        "53",
		"network":     "udp",
		"outboundTag": "dns_out",
	})
	coreRouterConfig := &coreConf.RouterConfig{
		RuleList:       []json.RawMessage{dnsRule},
		DomainStrategy: &domainStrategy,
	}

	for _, info := range infos {
		if len(info.Common.Routes) == 0 {
			continue
		}
		for _, route := range info.Common.Routes {
			switch route.Action {
			case "dns":
				if route.ActionValue == nil {
					continue
				}
				server := &coreConf.NameServerConfig{
					Address: &coreConf.Address{
						Address: xnet.ParseAddress(*route.ActionValue),
					},
				}
				if len(route.Match) != 0 {
					server.Domains = route.Match
					server.SkipFallback = true
				}
				coreDnsConfig.Servers = append(coreDnsConfig.Servers, server)
			case "block":
				rule := map[string]interface{}{
					"inboundTag":  info.Tag,
					"domain":      route.Match,
					"outboundTag": "block",
				}
				rawRule, err := json.Marshal(rule)
				if err != nil {
					continue
				}
				coreRouterConfig.RuleList = append(coreRouterConfig.RuleList, rawRule)
			case "block_ip":
				rule := map[string]interface{}{
					"inboundTag":  info.Tag,
					"ip":          route.Match,
					"outboundTag": "block",
				}
				rawRule, err := json.Marshal(rule)
				if err != nil {
					continue
				}
				coreRouterConfig.RuleList = append(coreRouterConfig.RuleList, rawRule)
			case "block_port":
				rule := map[string]interface{}{
					"inboundTag":  info.Tag,
					"port":        strings.Join(route.Match, ","),
					"outboundTag": "block",
				}
				rawRule, err := json.Marshal(rule)
				if err != nil {
					continue
				}
				coreRouterConfig.RuleList = append(coreRouterConfig.RuleList, rawRule)
			case "protocol":
				rule := map[string]interface{}{
					"inboundTag":  info.Tag,
					"protocol":    route.Match,
					"outboundTag": "block",
				}
				rawRule, err := json.Marshal(rule)
				if err != nil {
					continue
				}
				coreRouterConfig.RuleList = append(coreRouterConfig.RuleList, rawRule)
			case "route":
				coreOutboundConfig, coreRouterConfig.RuleList = addCustomOutboundRoute(coreOutboundConfig, coreRouterConfig.RuleList, info.Tag, route, "domain", route.Match, !allowAll)
			case "route_ip":
				coreOutboundConfig, coreRouterConfig.RuleList = addCustomOutboundRoute(coreOutboundConfig, coreRouterConfig.RuleList, info.Tag, route, "ip", route.Match, !allowAll)
			case "default_out":
				coreOutboundConfig, coreRouterConfig.RuleList = addCustomOutboundRoute(coreOutboundConfig, coreRouterConfig.RuleList, info.Tag, route, "network", "tcp,udp", !allowAll)
			default:
				log.WithFields(log.Fields{"tag": info.Tag, "route": route.Id, "action": route.Action}).Warn("Skip route with unknown action")
			}
		}
	}
	if len(allowTags) > 0 {
		allowOutbound, err := buildFreedomOutbound(allowPrivateOutboundTag, true)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("build private egress outbound: %w", err)
		}
		coreOutboundConfig = append(coreOutboundConfig, allowOutbound)
		// Appended last so the panel's routes keep precedence.
		rawRule, err := json.Marshal(map[string]interface{}{
			"inboundTag":  allowTags,
			"network":     "tcp,udp",
			"outboundTag": allowPrivateOutboundTag,
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("marshal private egress rule: %w", err)
		}
		coreRouterConfig.RuleList = append(coreRouterConfig.RuleList, rawRule)
	}
	DnsConfig, err := coreDnsConfig.Build()
	if err != nil {
		return nil, nil, nil, err
	}
	RouterConfig, err := coreRouterConfig.Build()
	if err != nil {
		return nil, nil, nil, err
	}
	return DnsConfig, coreOutboundConfig, RouterConfig, nil
}

// addCustomOutboundRoute handles the route, route_ip and default_out actions:
// action_value holds an Xray outbound whose tag the new rule points at. The
// outbound is built before the rule is added, so a route whose outbound Xray
// rejects is skipped (and logged) instead of pointing at a missing tag.
//
// Custom freedom outbounds are shared by tag across nodes, so they block private
// destinations unless every node in the core allows private egress.
func addCustomOutboundRoute(outbounds []*core.OutboundHandlerConfig, rules []json.RawMessage, inboundTag string, route panel.Route, matchKey string, matchValue interface{}, blockPrivate bool) ([]*core.OutboundHandlerConfig, []json.RawMessage) {
	logger := log.WithFields(log.Fields{"tag": inboundTag, "route": route.Id, "action": route.Action})
	if route.ActionValue == nil {
		logger.Warn("Skip route without an outbound")
		return outbounds, rules
	}
	outbound := &coreConf.OutboundDetourConfig{}
	if err := json.Unmarshal([]byte(*route.ActionValue), outbound); err != nil {
		logger.WithError(err).Error("Skip route: invalid outbound JSON")
		return outbounds, rules
	}
	if !hasOutboundWithTag(outbounds, outbound.Tag) {
		if blockPrivate && strings.EqualFold(outbound.Protocol, "freedom") {
			if err := prependPrivateBlock(outbound); err != nil {
				logger.WithError(err).Error("Skip route: invalid freedom settings")
				return outbounds, rules
			}
		}
		built, err := outbound.Build()
		if err != nil {
			logger.WithError(err).Error("Skip route: Xray rejected the outbound")
			return outbounds, rules
		}
		outbounds = append(outbounds, built)
	}
	rawRule, err := json.Marshal(map[string]interface{}{
		"inboundTag":  inboundTag,
		matchKey:      matchValue,
		"outboundTag": outbound.Tag,
	})
	if err != nil {
		logger.WithError(err).Error("Skip route: marshal rule")
		return outbounds, rules
	}
	return outbounds, append(rules, rawRule)
}

// prependPrivateBlock puts the private-range block rule first in a freedom
// outbound's finalRules, ahead of any rules the panel supplied.
func prependPrivateBlock(outbound *coreConf.OutboundDetourConfig) error {
	settings := map[string]json.RawMessage{}
	if outbound.Settings != nil && len(*outbound.Settings) > 0 && string(*outbound.Settings) != "null" {
		if err := json.Unmarshal(*outbound.Settings, &settings); err != nil {
			return err
		}
	}
	var rules []json.RawMessage
	if raw, ok := settings["finalRules"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &rules); err != nil {
			return fmt.Errorf("finalRules: %w", err)
		}
	}
	block, err := json.Marshal(map[string]interface{}{"action": "block", "ip": privateEgressRanges})
	if err != nil {
		return err
	}
	merged, err := json.Marshal(append([]json.RawMessage{block}, rules...))
	if err != nil {
		return err
	}
	settings["finalRules"] = merged
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	msg := json.RawMessage(raw)
	outbound.Settings = &msg
	return nil
}
