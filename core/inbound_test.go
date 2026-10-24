package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/proxy/vless/inbound"
	"github.com/youtonghy/TJXT_node/api/panel"
)

func testNode(t *testing.T, raw string) *panel.NodeInfo {
	t.Helper()
	common := &panel.CommonNode{ServerPort: 10443, ListenIP: "127.0.0.1", CertInfo: &panel.CertInfo{}}
	if err := json.Unmarshal([]byte(raw), common); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return &panel.NodeInfo{Id: 1, Type: common.Protocol, Tag: "test", Common: common}
}

// streamNetwork is the transport an inbound was built with.
func streamNetwork(t *testing.T, raw string) (string, error) {
	t.Helper()
	config, err := buildInbound(testNode(t, raw), "test", nil)
	if err != nil {
		return "", err
	}
	msg, err := config.ReceiverSettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	return msg.(*proxyman.ReceiverConfig).GetStreamSettings().GetProtocolName(), nil
}

// vless, vmess, trojan and anytls share one set of transports; missing, null or
// {} transport settings keep the transport's defaults.
func TestStreamProtocolsShareTransports(t *testing.T) {
	networks := map[string]string{
		"": "tcp", "tcp": "tcp", "raw": "tcp", "ws": "websocket", "grpc": "grpc",
		"httpupgrade": "httpupgrade", "xhttp": "splithttp", "splithttp": "splithttp",
	}
	for _, protocol := range []string{"vless", "vmess", "trojan", "anytls"} {
		for network, want := range networks {
			for _, settings := range []string{`null`, `{}`, `{"path":"/p"}`} {
				raw := `{"protocol":"` + protocol + `","network":"` + network + `","network_settings":` + settings + `}`
				if network == "" || network == "tcp" || network == "raw" {
					raw = `{"protocol":"` + protocol + `","network":"` + network + `","network_settings":` +
						strings.Replace(settings, `"path":"/p"`, `"acceptProxyProtocol":false`, 1) + `}`
				}
				got, err := streamNetwork(t, raw)
				if err != nil {
					t.Fatalf("%s: %v", raw, err)
				}
				if got != want {
					t.Fatalf("%s: transport %q, want %q", raw, got, want)
				}
			}
		}
		for _, network := range []string{"h2", "http", "quic", "bogus"} {
			_, err := streamNetwork(t, `{"protocol":"`+protocol+`","network":"`+network+`"}`)
			if err == nil || !strings.Contains(err.Error(), `"`+network+`"`) {
				t.Fatalf("%s over %s: expected an error naming the network, got %v", protocol, network, err)
			}
		}
	}
}

func TestVLessEncryptionNoneIsNoDecryption(t *testing.T) {
	for _, enc := range []string{"", "none", " None "} {
		config, err := buildInbound(testNode(t, `{"protocol":"vless","encryption":"`+enc+`"}`), "test", nil)
		if err != nil {
			t.Fatalf("%q: %v", enc, err)
		}
		msg, err := config.ProxySettings.GetInstance()
		if err != nil {
			t.Fatal(err)
		}
		if d := msg.(*inbound.Config).GetDecryption(); d != "none" {
			t.Fatalf("%q: decryption %q", enc, d)
		}
	}
	if _, err := buildInbound(testNode(t, `{"protocol":"vless","encryption":"aes"}`), "test", nil); err == nil {
		t.Fatal("expected an error for an unknown encryption")
	}
}

func TestShadowsocksCiphers(t *testing.T) {
	const key16 = "L8dsfbeHPFvIGfC9zOUbKA=="
	const key32 = "cdLSHTnnySASHCyTPm32WxiUpuVbtizNhrCYi4NdQuk="
	users := []panel.UserInfo{{Id: 1, Uuid: "00000000-0000-4000-8000-000000000001"}}
	for _, tc := range []struct{ cipher, key string }{
		{"aes-128-gcm", ""}, {"aes-256-gcm", ""}, {"AES-256-GCM", ""},
		{"chacha20-poly1305", ""}, {"chacha20-ietf-poly1305", ""},
		{"xchacha20-poly1305", ""}, {"xchacha20-ietf-poly1305", ""},
		{"2022-blake3-aes-128-gcm", key16}, {"2022-blake3-aes-256-gcm", key32},
		// A server key sent with a classic cipher is not used.
		{"aes-128-gcm", key16},
	} {
		raw := `{"protocol":"shadowsocks","cipher":"` + tc.cipher + `","server_key":"` + tc.key + `"}`
		if _, err := buildInbound(testNode(t, raw), "test", nil); err != nil {
			t.Fatalf("%s: %v", tc.cipher, err)
		}
		if _, err := buildSSUsers("test", users, tc.cipher, tc.key); err != nil {
			t.Fatalf("%s users: %v", tc.cipher, err)
		}
	}
	for _, tc := range []struct{ cipher, key, errText string }{
		{"aes-192-gcm", "", "unsupported"},
		{"none", "", "unsupported"},
		{"", "", "unsupported"},
		{"2022-blake3-chacha20-poly1305", key32, "single user"},
		{"2022-blake3-aes-128-gcm", "", "server_key"},
	} {
		raw := `{"protocol":"shadowsocks","cipher":"` + tc.cipher + `","server_key":"` + tc.key + `"}`
		if _, err := buildInbound(testNode(t, raw), "test", nil); err == nil || !strings.Contains(err.Error(), tc.errText) {
			t.Fatalf("%q: expected an error about %s, got %v", tc.cipher, tc.errText, err)
		}
		if _, err := buildSSUsers("test", users, tc.cipher, tc.key); err == nil {
			t.Fatalf("%q users: expected an error", tc.cipher)
		}
	}
}
