package conf

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestMachineOnlyConfigLoads(t *testing.T) {
	c := New()
	if err := c.LoadFromPath(writeConfig(t, `{"Machine":{"ApiHost":"http://panel","ApiKey":"m"}}`)); err != nil {
		t.Fatalf("machine-only config: %v", err)
	}
	if c.Machine == nil || c.Machine.Key != "m" || c.Machine.RetryCount == nil || len(c.NodeConfigs) != 0 {
		t.Fatalf("machine = %+v nodes = %+v", c.Machine, c.NodeConfigs)
	}
	if n := c.Machine.Node(7); n.NodeID != 7 || n.Key != "m" || n.APIHost != "http://panel" {
		t.Fatalf("machine node = %+v", n)
	}
	for _, bad := range []string{
		`{"Machine":{"ApiKey":"m"}}`,
		`{"Machine":{"ApiHost":"http://panel"}}`,
		`{}`,
	} {
		if err := New().LoadFromPath(writeConfig(t, bad)); err == nil {
			t.Errorf("%s: want a validation error", bad)
		}
	}
}

func TestEnvironmentMachineKeyReplacesNodes(t *testing.T) {
	path := writeConfig(t, `{"Nodes":[{"ApiHost":"http://a","ApiKey":"1"}]}`)
	t.Setenv(EnvAPIHost, "http://env")
	t.Setenv(EnvMachineKey, "env-machine")
	c := New()
	if err := c.LoadFromPath(path); err != nil {
		t.Fatal(err)
	}
	if c.Machine == nil || c.Machine.APIHost != "http://env" || c.Machine.Key != "env-machine" || len(c.NodeConfigs) != 0 {
		t.Fatalf("machine = %+v nodes = %+v", c.Machine, c.NodeConfigs)
	}
	t.Setenv(EnvAPIKey, "node-key")
	if err := New().LoadFromPath(path); err == nil {
		t.Fatal("a machine key and a node key together must be refused")
	}
}

func TestWriteMachineDropsTheNodesItRuns(t *testing.T) {
	path := writeConfig(t, `{"Log":{"Level":"debug"},"PprofPort":6060,"Nodes":[
		{"ApiHost":"http://panel/","ApiKey":"own-5","NodeID":5},
		{"ApiHost":"http://panel","ApiKey":"m"},
		{"ApiHost":"http://panel","ApiKey":"own-8","NodeID":8},
		{"ApiHost":"http://other","ApiKey":"o","NodeID":5}]}`)
	m := MachineConfig{APIHost: "http://panel", Key: "m", Timeout: 15, Protocol: "auto"}
	if err := WriteMachine(path, m, []int{5, 6}, true, false); err != nil {
		t.Fatal(err)
	}
	c := New()
	if err := c.LoadFromPath(path); err != nil {
		t.Fatal(err)
	}
	if c.Machine == nil || c.Machine.Key != "m" || c.LogConfig.Level != "debug" || c.PprofPort != 6060 {
		t.Fatalf("config = %+v machine = %+v", c, c.Machine)
	}
	if len(c.NodeConfigs) != 2 || c.NodeConfigs[0].NodeID != 8 || c.NodeConfigs[1].APIHost != "http://other" {
		t.Fatalf("kept nodes = %+v", c.NodeConfigs)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode: %v %v", fi.Mode(), err)
	}

	// Pairing the machine again replaces it; without keeping nodes Nodes goes,
	// which needs force once several nodes are left.
	if err := WriteMachine(path, m, []int{5}, false, false); !errors.Is(err, ErrMultiNodeConfig) {
		t.Fatalf("want ErrMultiNodeConfig, got %v", err)
	}
	if err := WriteMachine(path, m, []int{5}, false, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Nodes") || strings.Count(string(raw), "Machine") != 1 {
		t.Fatalf("config after replace:\n%s", raw)
	}
	// A node can still be added next to the machine.
	if err := AppendNode(path, NodeConfig{APIHost: "http://third", Key: "t", NodeID: 3}); err != nil {
		t.Fatalf("append to a machine-only config: %v", err)
	}
}
