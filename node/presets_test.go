package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The panel's recommended presets, as the panel delivers them (exported from
// TONG-ladder with TJXT_EXPORT_NODE_PRESETS, see testdata/presets/README.md),
// start a real Xray inbound. This is the contract check run on every Xray
// upgrade: a release that refuses one of these configs must not ship.
func TestPanelPresetsStartXray(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "presets", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no preset fixtures: %v", err)
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			startPanelConfig(t, name, config)
		})
	}
}

// Panel configs beyond the presets, in the shapes older and newer panels send,
// start a real Xray inbound too.
func TestPanelConfigsStartXray(t *testing.T) {
	configs := map[string]string{
		"trojan-without-tls-gets-self-signed": `{"protocol":"trojan","tls":0,"network":"tcp"}`,
		"trojan-ws":                           `{"protocol":"trojan","tls":1,"tls_settings":{"cert_mode":"self"},"network":"ws","network_settings":{"path":"/ws"}}`,
		"trojan-xhttp":                        `{"protocol":"trojan","tls":1,"network":"xhttp","network_settings":null}`,
		"trojan-httpupgrade":                  `{"protocol":"trojan","tls":1,"network":"httpupgrade","network_settings":{}}`,
		"anytls-without-tls":                  `{"protocol":"anytls","tls":0,"network":""}`,
		"tuic-without-cert-mode":              `{"protocol":"tuic","tls":1,"congestion_control":"bbr","tls_settings":{"cert_mode":""}}`,
		"hysteria2-string-bandwidth":          `{"protocol":"hysteria2","tls":1,"up_mbps":"100","down_mbps":"200","tls_settings":{"cert_mode":"self","reject_unknown_sni":"0"}}`,
		"vless-ws-encryption-none":            `{"protocol":"vless","tls":0,"encryption":"none","network":"ws","network_settings":{}}`,
		"vmess-tls-none-terminated-in-front":  `{"protocol":"vmess","tls":1,"tls_settings":{"cert_mode":"none"},"network":"grpc","network_settings":{"serviceName":"g"}}`,
		"vless-reality-numeric-port":          `{"protocol":"vless","tls":2,"network":"tcp","tls_settings":{"dest":"cover.example","server_port":443,"xver":"0","server_names":["cover.example"],"private_key":"ICYcTS2hPDtH2J7yXqquiFz0w3oeXhWVWDFvk4K9UVE","short_ids":[""]}}`,
		"shadowsocks-xchacha20":               `{"protocol":"shadowsocks","cipher":"xchacha20-ietf-poly1305"}`,
		"shadowsocks-2022-aes-256":            `{"protocol":"shadowsocks","cipher":"2022-blake3-aes-256-gcm","server_key":"cdLSHTnnySASHCyTPm32WxiUpuVbtizNhrCYi4NdQuk="}`,
	}
	for name, raw := range configs {
		t.Run(name, func(t *testing.T) {
			var config map[string]any
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			config["host"] = "node.example.com"
			config["base_config"] = map[string]any{"push_interval": 3600, "pull_interval": 3600}
			startPanelConfig(t, name, config)
		})
	}
}

// startPanelConfig starts a test controller with a node config as the panel
// sends it and checks that its inbound runs with its users.
func startPanelConfig(t *testing.T, name string, config map[string]any) {
	t.Helper()
	p := newFakePanel(t, "", testUsers(1, 2))
	// Local listen address and port.
	port := freePort(t)
	config["listen_ip"] = "127.0.0.1"
	// The test controller is node 1.
	config["id"] = 1
	if base, ok := config["base_config"].(map[string]any); ok {
		base["node_id"] = 1
	}
	config["server_port"] = port
	config["port"] = port
	// Certificates in a temporary directory, also for protocols that use TLS
	// with "tls": 0.
	if config["tls"] != float64(2) {
		tls, ok := config["tls_settings"].(map[string]any)
		if !ok {
			tls = map[string]any{}
			config["tls_settings"] = tls
		}
		dir := t.TempDir()
		tls["cert_file"] = filepath.Join(dir, "node.cer")
		tls["key_file"] = filepath.Join(dir, "node.key")
	}
	p.mu.Lock()
	p.config = config
	p.mu.Unlock()
	ctrl, _ := startTestController(t, p)
	if len(ctrl.userList) != 2 {
		t.Fatalf("%s: inbound did not start with its users (%d)", name, len(ctrl.userList))
	}
}
