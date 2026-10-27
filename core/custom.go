package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
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

// nodeBinding selects one node's traffic in routing rules: by its inbound, or
// for the nodes of a shared inbound (whose users share one Xray inbound) by its
// users, whose emails start with the node tag.
type nodeBinding struct {
	tag    string
	byUser bool
}

// userPattern matches the emails of a node's users (format.UserTag).
func userPattern(tag string) string {
	return "regexp:^" + regexp.QuoteMeta(tag) + `\|`
}

func (b nodeBinding) bind(ruleMap map[string]json.RawMessage, logger *log.Entry) {
	if !b.byUser {
		raw, _ := json.Marshal(b.tag)
		ruleMap["inboundTag"] = raw
		return
	}
	if _, ok := ruleMap["user"]; ok {
		logger.Warn("Route of a node sharing an inbound: its user match is replaced by the node's users")
	}
	raw, _ := json.Marshal([]string{userPattern(b.tag)})
	ruleMap["user"] = raw
}

// nodeBindings returns how each node's rules select its traffic: by user for
// the nodes of shared inbounds (the lines and the nodes they share), else by
// inbound.
func nodeBindings(infos []*panel.NodeInfo) map[string]nodeBinding {
	sharedInbounds := map[string]bool{}
	for _, info := range infos {
		if info != nil && info.Shared() {
			sharedInbounds[info.Inbound()] = true
		}
	}
	bindings := make(map[string]nodeBinding, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		bindings[info.Tag] = nodeBinding{tag: info.Tag, byUser: info.Shared() || sharedInbounds[info.Tag]}
	}
	return bindings
}

// routingMatchKeys are the match fields supported by Xray's RawFieldRule.
var routingMatchKeys = []string{
	"domain", "domains", "ip", "port", "network",
	"sourceIP", "source", "sourcePort", "user", "vlessRoute",
	"attrs", "localIP", "localPort", "process", "localOS", "webhook",
}

// outboundFieldKeys are the fields belonging to coreConf.OutboundDetourConfig.
var outboundFieldKeys = []string{
	"protocol", "sendThrough", "tag", "settings",
	"streamSettings", "proxySettings", "mux", "targetStrategy",
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
	dnsOut, err := buildDnsOutbound()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build dns outbound: %w", err)
	}
	coreOutboundConfig = append(coreOutboundConfig, dnsOut)

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

	blockPrivate := !allowAll
	bindings := nodeBindings(infos)
	for _, info := range infos {
		if info == nil || info.Common == nil || len(info.Common.Routes) == 0 {
			continue
		}
		coreDnsConfig.Servers, coreOutboundConfig, coreRouterConfig.RuleList = applyNodeRoutes(
			coreDnsConfig.Servers,
			coreOutboundConfig,
			coreRouterConfig.RuleList,
			bindings[info.Tag],
			info.Common.Routes,
			blockPrivate,
		)
	}
	if len(allowTags) > 0 {
		allowOutbound, err := buildFreedomOutbound(allowPrivateOutboundTag, true)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("build private egress outbound: %w", err)
		}
		coreOutboundConfig = append(coreOutboundConfig, allowOutbound)
		// Appended last so the panel's routes keep precedence. Nodes of shared
		// inbounds are selected by their users, the others by their inbound.
		var inboundTags, users []string
		for _, tag := range allowTags {
			if bindings[tag].byUser {
				users = append(users, userPattern(tag))
			} else {
				inboundTags = append(inboundTags, tag)
			}
		}
		for _, match := range []struct {
			key    string
			values []string
		}{{"inboundTag", inboundTags}, {"user", users}} {
			if len(match.values) == 0 {
				continue
			}
			rawRule, err := json.Marshal(map[string]interface{}{
				match.key:     match.values,
				"network":     "tcp,udp",
				"outboundTag": allowPrivateOutboundTag,
			})
			if err != nil {
				return nil, nil, nil, fmt.Errorf("marshal private egress rule: %w", err)
			}
			coreRouterConfig.RuleList = append(coreRouterConfig.RuleList, rawRule)
		}
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

type pendingNodeRule struct {
	routeID        int
	ruleMap        map[string]json.RawMessage
	pureOutbound   bool
	targetOutbound string
}

// applyNodeRoutes applies a node's stacked outbound/routing rules in panel order.
func applyNodeRoutes(
	dnsServers []*coreConf.NameServerConfig,
	outbounds []*core.OutboundHandlerConfig,
	rules []json.RawMessage,
	node nodeBinding,
	routes []panel.Route,
	blockPrivate bool,
) ([]*coreConf.NameServerConfig, []*core.OutboundHandlerConfig, []json.RawMessage) {
	var pending []pendingNodeRule
	referencedTags := map[string]bool{}

	for _, route := range routes {
		raw := routeRuleJSON(route)
		if len(raw) == 0 {
			continue
		}
		dnsServers, outbounds, pending = collectRouteItems(
			dnsServers,
			outbounds,
			pending,
			referencedTags,
			node,
			route.Id,
			raw,
			blockPrivate,
		)
	}

	for _, item := range pending {
		logger := log.WithFields(log.Fields{"tag": node.tag, "route": item.routeID})
		if item.pureOutbound {
			// A pure outbound object (no match fields) acts as a catch-all rule at its
			// position unless another rule on this node explicitly routes to its tag.
			if referencedTags[item.targetOutbound] {
				continue
			}
		}
		if item.targetOutbound != "" && !hasOutboundWithTag(outbounds, item.targetOutbound) {
			logger.WithField("outboundTag", item.targetOutbound).Warn("Skip route: target outbound is not registered")
			continue
		}
		rawRule, err := json.Marshal(item.ruleMap)
		if err != nil {
			logger.WithError(err).Error("Skip route: marshal rule")
			continue
		}
		if _, err := (&coreConf.RouterConfig{RuleList: []json.RawMessage{rawRule}}).Build(); err != nil {
			logger.WithError(err).Error("Skip route: Xray rejected the routing rule")
			continue
		}
		rules = append(rules, rawRule)
	}

	return dnsServers, outbounds, rules
}

// routeRuleJSON returns the Xray JSON payload for a panel Route, synthesizing
// one from legacy action/match/action_value fields when talking to an older panel.
func routeRuleJSON(route panel.Route) json.RawMessage {
	trimmedRule := bytes.TrimSpace(route.Rule)
	if len(trimmedRule) > 0 && !bytes.Equal(trimmedRule, []byte("null")) {
		return trimmedRule
	}
	action := strings.TrimSpace(route.Action)
	var val string
	if route.ActionValue != nil {
		val = strings.TrimSpace(*route.ActionValue)
	}
	if action == "" || action == "xray" {
		if val == "" {
			return nil
		}
		return json.RawMessage(val)
	}

	switch action {
	case "dns":
		if val == "" {
			return nil
		}
		dnsObj := map[string]interface{}{"address": val}
		if len(route.Match) > 0 {
			dnsObj["domains"] = route.Match
			dnsObj["skipFallback"] = true
		}
		raw, _ := json.Marshal(map[string]interface{}{"dns": dnsObj})
		return raw
	case "block":
		raw, _ := json.Marshal(map[string]interface{}{"domain": route.Match, "outboundTag": "block"})
		return raw
	case "block_ip":
		raw, _ := json.Marshal(map[string]interface{}{"ip": route.Match, "outboundTag": "block"})
		return raw
	case "block_port":
		raw, _ := json.Marshal(map[string]interface{}{"port": strings.Join(route.Match, ","), "outboundTag": "block"})
		return raw
	case "protocol":
		raw, _ := json.Marshal(map[string]interface{}{"protocol": route.Match, "outboundTag": "block"})
		return raw
	case "route", "route_ip":
		if val == "" {
			log.WithFields(log.Fields{"route": route.Id, "action": action}).Warn("Skip route without an outbound")
			return nil
		}
		matchKey := "domain"
		if action == "route_ip" {
			matchKey = "ip"
		}
		var outboundObj map[string]json.RawMessage
		if json.Unmarshal([]byte(val), &outboundObj) == nil && len(outboundObj) > 0 {
			raw, _ := json.Marshal(map[string]interface{}{
				matchKey:   route.Match,
				"outbound": json.RawMessage(val),
			})
			return raw
		}
		raw, _ := json.Marshal(map[string]interface{}{
			matchKey:      route.Match,
			"outboundTag": val,
		})
		return raw
	case "default_out":
		if val == "" {
			log.WithFields(log.Fields{"route": route.Id, "action": action}).Warn("Skip route without an outbound")
			return nil
		}
		return json.RawMessage(val)
	default:
		log.WithFields(log.Fields{"route": route.Id, "action": action}).Warn("Skip route with unknown action")
		return nil
	}
}

func collectRouteItems(
	dnsServers []*coreConf.NameServerConfig,
	outbounds []*core.OutboundHandlerConfig,
	pending []pendingNodeRule,
	referencedTags map[string]bool,
	node nodeBinding,
	routeID int,
	raw json.RawMessage,
	blockPrivate bool,
) ([]*coreConf.NameServerConfig, []*core.OutboundHandlerConfig, []pendingNodeRule) {
	logger := log.WithFields(log.Fields{"tag": node.tag, "route": routeID})
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return dnsServers, outbounds, pending
	}

	if trimmed[0] == '[' {
		var list []json.RawMessage
		if err := json.Unmarshal(trimmed, &list); err != nil {
			logger.WithError(err).Error("Skip route: invalid JSON array")
			return dnsServers, outbounds, pending
		}
		for _, elem := range list {
			dnsServers, outbounds, pending = collectRouteItems(
				dnsServers,
				outbounds,
				pending,
				referencedTags,
				node,
				routeID,
				elem,
				blockPrivate,
			)
		}
		return dnsServers, outbounds, pending
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		logger.WithError(err).Error("Skip route: invalid JSON object")
		return dnsServers, outbounds, pending
	}
	if len(obj) == 0 {
		return dnsServers, outbounds, pending
	}

	// 1. Composite Xray fragment containing "dns", "outbounds", "rules", or "routing".
	_, hasDNS := obj["dns"]
	_, hasOutbounds := obj["outbounds"]
	_, hasRules := obj["rules"]
	_, hasRouting := obj["routing"]
	if hasDNS || hasOutbounds || hasRules || hasRouting {
		if rawDNS, ok := obj["dns"]; ok {
			dnsServers = appendDNSFromJSON(dnsServers, rawDNS, logger)
		}
		// As in Xray, the fragment's first outbound takes the traffic its own rules
		// leave unmatched.
		var firstOutbound string
		if rawOutbounds, ok := obj["outbounds"]; ok {
			var obList []json.RawMessage
			if err := json.Unmarshal(rawOutbounds, &obList); err != nil {
				logger.WithError(err).Error("Skip route: invalid outbounds array")
			} else {
				for idx, rawOb := range obList {
					defaultTag := fmt.Sprintf("route_%d_%d", routeID, idx)
					var tag string
					var ok bool
					outbounds, tag, ok = registerCustomOutbound(outbounds, rawOb, defaultTag, blockPrivate, logger)
					if idx == 0 && ok {
						firstOutbound = tag
					}
				}
			}
		}
		if rawRules, ok := obj["rules"]; ok {
			dnsServers, outbounds, pending = collectRouteItems(
				dnsServers, outbounds, pending, referencedTags, node, routeID, rawRules, blockPrivate,
			)
		}
		if rawRouting, ok := obj["routing"]; ok {
			var routingObj struct {
				Rules []json.RawMessage `json:"rules"`
			}
			if err := json.Unmarshal(rawRouting, &routingObj); err != nil {
				logger.WithError(err).Error("Skip route: invalid routing object")
			} else {
				for _, r := range routingObj.Rules {
					dnsServers, outbounds, pending = collectRouteItems(
						dnsServers, outbounds, pending, referencedTags, node, routeID, r, blockPrivate,
					)
				}
			}
		}
		if firstOutbound != "" {
			pending = append(pending, outboundRule(node, routeID, firstOutbound, nil, logger))
		}
		return dnsServers, outbounds, pending
	}

	// 2. Routing rule object (has outboundTag, balancerTag, or inline outbound).
	_, hasOutboundTag := obj["outboundTag"]
	_, hasBalancerTag := obj["balancerTag"]
	rawInlineOutbound, hasInlineOutbound := obj["outbound"]
	if hasOutboundTag || hasBalancerTag || hasInlineOutbound {
		ruleMap := make(map[string]json.RawMessage, len(obj)+1)
		for k, v := range obj {
			if k != "outbound" {
				ruleMap[k] = v
			}
		}
		var targetTag string
		if hasInlineOutbound {
			defaultTag := fmt.Sprintf("route_%d", routeID)
			var ok bool
			outbounds, targetTag, ok = registerCustomOutbound(outbounds, rawInlineOutbound, defaultTag, blockPrivate, logger)
			if !ok {
				return dnsServers, outbounds, pending
			}
			tagBytes, _ := json.Marshal(targetTag)
			ruleMap["outboundTag"] = tagBytes
		} else if hasOutboundTag {
			var rawTag string
			if err := json.Unmarshal(obj["outboundTag"], &rawTag); err != nil || strings.TrimSpace(rawTag) == "" {
				logger.Error("Skip route: invalid outboundTag")
				return dnsServers, outbounds, pending
			}
			targetTag = normalizeOutboundTag(rawTag)
			tagBytes, _ := json.Marshal(targetTag)
			ruleMap["outboundTag"] = tagBytes
			referencedTags[targetTag] = true
		}
		node.bind(ruleMap, logger)
		pending = append(pending, pendingNodeRule{
			routeID:        routeID,
			ruleMap:        ruleMap,
			pureOutbound:   false,
			targetOutbound: targetTag,
		})
		return dnsServers, outbounds, pending
	}

	// 3. Xray Outbound object (has "protocol" string, optionally accompanied by routing match fields).
	var proto string
	if rawProto, ok := obj["protocol"]; ok && json.Unmarshal(rawProto, &proto) == nil && strings.TrimSpace(proto) != "" {
		outboundMap := make(map[string]json.RawMessage)
		for _, k := range outboundFieldKeys {
			if v, exists := obj[k]; exists {
				outboundMap[k] = v
			}
		}
		rawOutbound, err := json.Marshal(outboundMap)
		if err != nil {
			logger.WithError(err).Error("Skip route: marshal outbound")
			return dnsServers, outbounds, pending
		}
		defaultTag := fmt.Sprintf("route_%d", routeID)
		var tag string
		var ok bool
		outbounds, tag, ok = registerCustomOutbound(outbounds, rawOutbound, defaultTag, blockPrivate, logger)
		if !ok {
			return dnsServers, outbounds, pending
		}

		ruleMap := make(map[string]json.RawMessage)
		for _, k := range routingMatchKeys {
			if v, exists := obj[k]; exists {
				ruleMap[k] = v
			}
		}
		pending = append(pending, outboundRule(node, routeID, tag, ruleMap, logger))
		return dnsServers, outbounds, pending
	}

	logger.Warn("Skip route: unrecognized Xray rule JSON")
	return dnsServers, outbounds, pending
}

// outboundRule routes the node's traffic matching ruleMap to tag. Without match
// fields it catches all traffic and is pure: dropped when another rule on the
// node routes to tag explicitly.
func outboundRule(node nodeBinding, routeID int, tag string, ruleMap map[string]json.RawMessage, logger *log.Entry) pendingNodeRule {
	pure := len(ruleMap) == 0
	if ruleMap == nil {
		ruleMap = make(map[string]json.RawMessage)
	}
	if pure {
		netBytes, _ := json.Marshal("tcp,udp")
		ruleMap["network"] = netBytes
	}
	node.bind(ruleMap, logger)
	tagBytes, _ := json.Marshal(tag)
	ruleMap["outboundTag"] = tagBytes
	return pendingNodeRule{
		routeID:        routeID,
		ruleMap:        ruleMap,
		pureOutbound:   pure,
		targetOutbound: tag,
	}
}

func normalizeOutboundTag(tag string) string {
	trimmed := strings.TrimSpace(tag)
	switch strings.ToLower(trimmed) {
	case "direct", "default":
		return DefaultOutboundTag
	case "blackhole":
		return "block"
	default:
		return trimmed
	}
}

func appendDNSFromJSON(servers []*coreConf.NameServerConfig, raw json.RawMessage, logger *log.Entry) []*coreConf.NameServerConfig {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return servers
	}
	if trimmed[0] == '[' {
		var list []json.RawMessage
		if err := json.Unmarshal(trimmed, &list); err != nil {
			logger.WithError(err).Error("Skip route: invalid dns array")
			return servers
		}
		for _, item := range list {
			servers = appendDNSFromJSON(servers, item, logger)
		}
		return servers
	}
	var wrapper struct {
		Address      string   `json:"address"`
		Domains      []string `json:"domains"`
		SkipFallback bool     `json:"skipFallback"`
	}
	if err := json.Unmarshal(trimmed, &wrapper); err == nil && strings.TrimSpace(wrapper.Address) != "" {
		srv := &coreConf.NameServerConfig{
			Address: &coreConf.Address{
				Address: xnet.ParseAddress(strings.TrimSpace(wrapper.Address)),
			},
		}
		if len(wrapper.Domains) > 0 {
			srv.Domains = wrapper.Domains
			srv.SkipFallback = true
		} else if wrapper.SkipFallback {
			srv.SkipFallback = true
		}
		return append(servers, srv)
	}
	var srv coreConf.NameServerConfig
	if err := json.Unmarshal(trimmed, &srv); err != nil || srv.Address == nil {
		logger.Error("Skip route: invalid dns server JSON")
		return servers
	}
	return append(servers, &srv)
}

// registerCustomOutbound builds and appends an Xray outbound if its tag is not
// already present. Custom freedom outbounds are shared by tag across nodes, so
// they block private destinations unless every node in the core allows private egress.
func registerCustomOutbound(
	outbounds []*core.OutboundHandlerConfig,
	rawOutbound json.RawMessage,
	defaultTag string,
	blockPrivate bool,
	logger *log.Entry,
) ([]*core.OutboundHandlerConfig, string, bool) {
	outbound := &coreConf.OutboundDetourConfig{}
	if err := json.Unmarshal(rawOutbound, outbound); err != nil {
		logger.WithError(err).Error("Skip route: invalid outbound JSON")
		return outbounds, "", false
	}
	outbound.Tag = strings.TrimSpace(outbound.Tag)
	if outbound.Tag == "" {
		outbound.Tag = defaultTag
	}
	if !hasOutboundWithTag(outbounds, outbound.Tag) {
		if strings.EqualFold(outbound.Protocol, "freedom") {
			if err := applyPrivateEgressPolicy(outbound, blockPrivate); err != nil {
				logger.WithError(err).Error("Skip route: invalid freedom settings")
				return outbounds, "", false
			}
		}
		built, err := outbound.Build()
		if err != nil {
			logger.WithError(err).Error("Skip route: Xray rejected the outbound")
			return outbounds, "", false
		}
		outbounds = append(outbounds, built)
	}
	return outbounds, outbound.Tag, true
}

// applyPrivateEgressPolicy makes a panel freedom outbound follow the node's
// private-egress policy like the default outbound: blocking puts the
// private-range block rule ahead of the panel's finalRules; allowing adds an
// explicit allow when the panel set none, since Xray otherwise blocks private
// destinations for vless/vmess/trojan/shadowsocks/hysteria/wireguard inbounds.
func applyPrivateEgressPolicy(outbound *coreConf.OutboundDetourConfig, blockPrivate bool) error {
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
	if !blockPrivate {
		if len(rules) > 0 {
			return nil
		}
		allow, err := json.Marshal(map[string]string{"action": "allow"})
		if err != nil {
			return err
		}
		rules = []json.RawMessage{allow}
	} else {
		block, err := json.Marshal(map[string]interface{}{"action": "block", "ip": privateEgressRanges})
		if err != nil {
			return err
		}
		rules = append([]json.RawMessage{block}, rules...)
	}
	merged, err := json.Marshal(rules)
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
