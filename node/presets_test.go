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
			p := newFakePanel(t, "", testUsers(1, 2))
			// Local listen address and port, certificates in a temporary directory.
			port := freePort(t)
			config["listen_ip"] = "127.0.0.1"
			// The test controller is node 1.
			config["id"] = 1
			if base, ok := config["base_config"].(map[string]any); ok {
				base["node_id"] = 1
			}
			config["server_port"] = port
			config["port"] = port
			if tls, ok := config["tls_settings"].(map[string]any); ok && config["tls"] == float64(1) {
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
		})
	}
}
