package conf

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteSingleNodeKeepsSettingsAndIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-dir")
	path := filepath.Join(dir, "config.json")
	n := NodeConfig{APIHost: "http://panel", Key: "k", NodeID: 42, Timeout: 15, Protocol: "auto"}
	if err := WriteSingleNode(path, n, false); err != nil {
		t.Fatalf("fresh write: %v", err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("config mode: %v %v", fi.Mode(), err)
		}
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o750 {
			t.Fatalf("dir mode: %v %v", fi.Mode(), err)
		}
	}
	c := New()
	if err := c.LoadFromPath(path); err != nil || c.NodeConfigs[0].NodeID != 42 || c.LogConfig.Level != "info" {
		t.Fatalf("written config does not load: %+v %+v %v", c.NodeConfigs, c.LogConfig, err)
	}

	if err := os.WriteFile(path, []byte(`{"Log":{"Level":"debug","Output":"/var/log/x.log"},"PprofPort":6060,"Nodes":[{"ApiHost":"http://old","ApiKey":"o","NodeID":1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteSingleNode(path, n, false); err != nil {
		t.Fatalf("replace single node: %v", err)
	}
	c = New()
	if err := c.LoadFromPath(path); err != nil {
		t.Fatal(err)
	}
	if c.LogConfig.Level != "debug" || c.LogConfig.Output != "/var/log/x.log" || c.PprofPort != 6060 {
		t.Fatalf("settings not kept: %+v pprof=%d", c.LogConfig, c.PprofPort)
	}
	if len(c.NodeConfigs) != 1 || c.NodeConfigs[0].APIHost != "http://panel" {
		t.Fatalf("nodes not replaced: %+v", c.NodeConfigs)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestWriteSingleNodeRefusesMultiNodeWithoutForce(t *testing.T) {
	multi := `{"Log":{"Level":"warn"},"Nodes":[{"ApiHost":"http://a","ApiKey":"1"},{"ApiHost":"http://b","ApiKey":"2"}]}`
	path := writeConfig(t, multi)
	n := NodeConfig{APIHost: "http://panel", Key: "k", NodeID: 42}
	if err := WriteSingleNode(path, n, false); !errors.Is(err, ErrMultiNodeConfig) {
		t.Fatalf("want ErrMultiNodeConfig, got %v", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != multi {
		t.Fatal("refused write changed the file")
	}
	if err := WriteSingleNode(path, n, true); err != nil {
		t.Fatalf("forced write: %v", err)
	}
	var doc struct {
		Log   map[string]string
		Nodes []map[string]any
	}
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Nodes) != 1 || doc.Log["Level"] != "warn" {
		t.Fatalf("forced write: %s %v", raw, err)
	}

	broken := writeConfig(t, `{not json`)
	if err := WriteSingleNode(broken, n, false); err == nil {
		t.Fatal("an unparsable config must not be replaced without force")
	}
	if err := WriteSingleNode(broken, n, true); err != nil {
		t.Fatalf("forced write over a broken file: %v", err)
	}
}
