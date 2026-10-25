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

func TestAppendNodePreservesExistingNodesAndSettings(t *testing.T) {
	path := writeConfig(t, `{"Log":{"Level":"debug"},"PprofPort":6060,"Nodes":[{"ApiHost":"http://old","ApiKey":"old-key","NodeID":1}]}`)
	n := NodeConfig{APIHost: "http://new", Key: "new-key", NodeID: 2, Timeout: 15, Protocol: "auto"}
	if err := AppendNode(path, n); err != nil {
		t.Fatalf("append: %v", err)
	}
	c := New()
	if err := c.LoadFromPath(path); err != nil {
		t.Fatal(err)
	}
	if c.LogConfig.Level != "debug" || c.PprofPort != 6060 || len(c.NodeConfigs) != 2 {
		t.Fatalf("settings or nodes changed: %+v pprof=%d", c.LogConfig, c.PprofPort)
	}
	if c.NodeConfigs[1].Key != "new-key" || c.NodeConfigs[1].NodeID != 2 {
		t.Fatalf("new node missing: %+v", c.NodeConfigs)
	}
}

func TestMatchesNodeReadsJSONWithoutJSONExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json.pairing")
	if err := os.WriteFile(path, []byte(`{"Nodes":[{"ApiHost":"http://old","ApiKey":"old-key","NodeID":1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := MatchesNode(path, NodeConfig{APIHost: "http://old", Key: "old-key", NodeID: 1})
	if err != nil || !ok {
		t.Fatalf("want match, got %v, %v", ok, err)
	}
}

func TestAppendNodeRejectsDuplicate(t *testing.T) {
	path := writeConfig(t, `{"Nodes":[{"ApiHost":"http://old","ApiKey":"old-key","NodeID":1}]}`)
	n := NodeConfig{APIHost: "http://old", Key: "old-key", NodeID: 1}
	if err := AppendNode(path, n); !errors.Is(err, ErrNodeAlreadyConfigured) {
		t.Fatalf("want duplicate error, got %v", err)
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
