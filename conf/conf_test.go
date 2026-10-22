package conf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNodeIDIsOptional(t *testing.T) {
	c := New()
	if err := c.LoadFromPath(writeConfig(t, `{"Nodes":[{"ApiHost":"http://panel","ApiKey":"k"}]}`)); err != nil {
		t.Fatalf("key-only node: %v", err)
	}
	if c.NodeConfigs[0].NodeID != 0 {
		t.Fatalf("NodeID = %d", c.NodeConfigs[0].NodeID)
	}
	legacy := New()
	if err := legacy.LoadFromPath(writeConfig(t, validConfig)); err != nil || legacy.NodeConfigs[0].NodeID != 1 {
		t.Fatalf("legacy config: %+v %v", legacy.NodeConfigs, err)
	}
	for _, bad := range []string{
		`{"Nodes":[{"ApiKey":"k"}]}`,
		`{"Nodes":[{"ApiHost":"http://panel"}]}`,
		`{"Nodes":[{"ApiHost":"http://panel","ApiKey":"k","NodeID":-1}]}`,
		`{"Nodes":[]}`,
	} {
		if err := New().LoadFromPath(writeConfig(t, bad)); err == nil {
			t.Errorf("%s: want a validation error", bad)
		}
	}
}

func TestEnvironmentReplacesNodes(t *testing.T) {
	path := writeConfig(t, `{"Log":{"Level":"debug"},"Nodes":[{"ApiHost":"http://a","ApiKey":"1"},{"ApiHost":"http://b","ApiKey":"2"}]}`)
	t.Setenv(EnvAPIHost, "http://env")
	t.Setenv(EnvAPIKey, "env-key")
	t.Setenv(EnvNodeID, "5")
	c := New()
	if err := c.LoadFromPath(path); err != nil {
		t.Fatal(err)
	}
	if len(c.NodeConfigs) != 1 || c.NodeConfigs[0].APIHost != "http://env" || c.NodeConfigs[0].Key != "env-key" || c.NodeConfigs[0].NodeID != 5 {
		t.Fatalf("env node not applied: %+v", c.NodeConfigs)
	}
	if c.LogConfig.Level != "debug" {
		t.Fatalf("file settings lost: %+v", c.LogConfig)
	}
	// Without a config file the environment alone is enough.
	missing := New()
	if err := missing.LoadFromPath(filepath.Join(t.TempDir(), "none.json")); err != nil || missing.NodeConfigs[0].APIHost != "http://env" {
		t.Fatalf("env without file: %+v %v", missing.NodeConfigs, err)
	}
	t.Setenv(EnvNodeID, "x")
	if err := New().LoadFromPath(path); err == nil {
		t.Fatal("invalid TJXT_NODE_ID must fail")
	}
	t.Setenv(EnvNodeID, "")
	t.Setenv(EnvAPIKey, "")
	if err := New().LoadFromPath(path); err == nil || !strings.Contains(err.Error(), EnvAPIKey) {
		t.Fatalf("host without key must fail, got %v", err)
	}
}
