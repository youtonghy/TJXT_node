package core

import (
	"encoding/json"
	"fmt"
	"strings"

	coreConf "github.com/xtls/xray-core/infra/conf"
	panel "github.com/youtonghy/TJXT_node/api/v2board"
	"github.com/youtonghy/TJXT_node/common/crypt"
	"github.com/youtonghy/TJXT_node/common/format"
)

func buildHTTP(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig, users []panel.UserInfo) error {
	inbound.Protocol = "http"
	if len(users) == 0 {
		return fmt.Errorf("http inbound refuses noauth: user list is empty")
	}
	settings := &coreConf.HTTPServerConfig{
		Users:       httpAccounts(users),
		Transparent: boolFromSettings(nodeInfo, "allowTransparent"),
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal http settings: %w", err)
	}
	inbound.Settings = (*json.RawMessage)(&raw)
	return nil
}

func buildMixed(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig, users []panel.UserInfo) error {
	inbound.Protocol = "mixed"
	if len(users) == 0 {
		return fmt.Errorf("mixed inbound refuses noauth: user list is empty")
	}
	udp := true
	if raw := settingValue(nodeInfo, "udp"); len(raw) > 0 {
		_ = json.Unmarshal(raw, &udp)
	}
	settings := &coreConf.SocksServerConfig{
		AuthMethod: "password",
		Users:      socksAccounts(users),
		UDP:        udp,
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal mixed settings: %w", err)
	}
	inbound.Settings = (*json.RawMessage)(&raw)
	return nil
}

func buildTunnel(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig) error {
	inbound.Protocol = "tunnel"
	settings := map[string]any{
		"network":        "tcp,udp",
		"followRedirect": false,
	}
	mergeJSONMap(settings, nodeInfo.Common.Settings)
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal tunnel settings: %w", err)
	}
	inbound.Settings = (*json.RawMessage)(&raw)
	return nil
}

func buildTun(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig) error {
	inbound.Protocol = "tun"
	settings := &coreConf.TunConfig{
		Name: "xray0",
		MTU:  1500,
	}
	if len(nodeInfo.Common.Settings) > 0 {
		_ = json.Unmarshal(nodeInfo.Common.Settings, settings)
	}
	if settings.Name == "" {
		settings.Name = "xray0"
	}
	if settings.MTU == 0 {
		settings.MTU = 1500
	}
	// Never rewrite host routing from panel defaults.
	settings.AutoSystemRoutingTable = nil
	settings.AutoOutboundsInterface = nil
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal tun settings: %w", err)
	}
	inbound.Settings = (*json.RawMessage)(&raw)
	return nil
}

func buildWireGuard(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig, tag string, users []panel.UserInfo) error {
	inbound.Protocol = "wireguard"
	secret := settingString(nodeInfo, "secretKey")
	private := crypt.WireGuardServerPrivate(nodeInfo.Common.Host, nodeInfo.Common.ServerPort, secret)
	settings := &coreConf.WireGuardConfig{
		SecretKey: crypt.KeyB64(private),
		MTU:       1420,
		Address:   []string{"10.0.0.1/32", "fd59:7153:2388:b5fd::1/128"},
		Peers:     wireGuardPeers(tag, users),
	}
	if mtu := intFromSettings(nodeInfo, "mtu"); mtu > 0 {
		settings.MTU = int32(mtu)
	}
	if raw := settingValue(nodeInfo, "address"); len(raw) > 0 {
		var addresses []string
		if err := json.Unmarshal(raw, &addresses); err == nil && len(addresses) > 0 {
			settings.Address = addresses
		}
	}
	// Xray v26.9 removed WireGuard domainStrategy (it only affected outbound
	// dialing); the panel's "domainStrategy" setting is no longer applicable.
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal wireguard settings: %w", err)
	}
	inbound.Settings = (*json.RawMessage)(&raw)
	return nil
}

func applyPanelStreamSettings(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig) error {
	raw := strings.TrimSpace(string(nodeInfo.Common.StreamSettings))
	if raw == "" || raw == "{}" || raw == "null" {
		return nil
	}
	var stream coreConf.StreamConfig
	if err := json.Unmarshal(nodeInfo.Common.StreamSettings, &stream); err != nil {
		return fmt.Errorf("unmarshal streamSettings: %w", err)
	}
	inbound.StreamSetting = &stream
	return nil
}

func applyPanelSniffing(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig) {
	raw := strings.TrimSpace(string(nodeInfo.Common.Sniffing))
	if raw == "" || raw == "{}" || raw == "null" {
		return
	}
	var sniff coreConf.SniffingConfig
	if err := json.Unmarshal(nodeInfo.Common.Sniffing, &sniff); err == nil {
		inbound.SniffingConfig = &sniff
	}
}

func httpAccounts(users []panel.UserInfo) []*coreConf.HTTPAccount {
	accounts := make([]*coreConf.HTTPAccount, 0, len(users))
	for _, user := range users {
		accounts = append(accounts, &coreConf.HTTPAccount{
			Username: user.Uuid,
			Password: user.Uuid,
		})
	}
	return accounts
}

func socksAccounts(users []panel.UserInfo) []*coreConf.SocksAccount {
	accounts := make([]*coreConf.SocksAccount, 0, len(users))
	for _, user := range users {
		accounts = append(accounts, &coreConf.SocksAccount{
			Username: user.Uuid,
			Password: user.Uuid,
		})
	}
	return accounts
}

func wireGuardPeers(tag string, users []panel.UserInfo) []*coreConf.WireGuardPeerConfig {
	peers := make([]*coreConf.WireGuardPeerConfig, 0, len(users))
	for _, user := range users {
		peers = append(peers, &coreConf.WireGuardPeerConfig{
			PublicKey:  crypt.KeyB64(crypt.WireGuardClientPublic(user.Uuid)),
			AllowedIPs: []string{crypt.WireGuardClientAddress(user.Uuid)},
			Email:      format.UserTag(tag, user.Uuid),
		})
	}
	return peers
}

func settingValue(nodeInfo *panel.NodeInfo, key string) json.RawMessage {
	if len(nodeInfo.Common.Settings) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(nodeInfo.Common.Settings, &object); err != nil {
		return nil
	}
	return object[key]
}

func settingString(nodeInfo *panel.NodeInfo, key string) string {
	raw := settingValue(nodeInfo, key)
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return strings.Trim(string(raw), `"`)
}

func boolFromSettings(nodeInfo *panel.NodeInfo, key string) bool {
	raw := settingValue(nodeInfo, key)
	if len(raw) == 0 {
		return false
	}
	var value bool
	_ = json.Unmarshal(raw, &value)
	return value
}

func intFromSettings(nodeInfo *panel.NodeInfo, key string) int {
	raw := settingValue(nodeInfo, key)
	if len(raw) == 0 {
		return 0
	}
	var value int
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return 0
}

func mergeJSONMap(dst map[string]any, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var extra map[string]any
	if err := json.Unmarshal(raw, &extra); err != nil {
		return
	}
	for key, value := range extra {
		dst[key] = value
	}
}

func RebuildsOnUserChange(nodeType string) bool {
	switch nodeType {
	case "http", "mixed", "wireguard":
		return true
	default:
		return false
	}
}
