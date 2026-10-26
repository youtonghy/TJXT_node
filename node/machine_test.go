package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/conf"
	"github.com/youtonghy/TJXT_node/core"
)

const testMachineKey = "machine-key-1234"

// machinePanel serves a machine's node list and the v1 endpoints of its nodes,
// accepting the machine key for the listed nodes only.
type machinePanel struct {
	*httptest.Server
	mu      sync.Mutex
	key     string
	nodes   []int
	configs map[int]map[string]any
	// users are the users per node (testUsers(1) for nodes not listed).
	users map[int][]panel.UserInfo
}

func newMachinePanel(t *testing.T) *machinePanel {
	t.Helper()
	p := &machinePanel{key: testMachineKey, configs: map[int]map[string]any{}, users: map[int][]panel.UserInfo{}}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

// addNode assigns a VLESS node listening on a free port to the machine.
func (p *machinePanel) addNode(t *testing.T, id int, extra map[string]any) {
	t.Helper()
	config := map[string]any{
		"protocol":    "vless",
		"listen_ip":   "127.0.0.1",
		"server_port": freePort(t),
		"base_config": map[string]any{"push_interval": 3600, "pull_interval": 3600, "node_id": id},
	}
	for k, v := range extra {
		config[k] = v
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes = append(p.nodes, id)
	p.configs[id] = config
}

func (p *machinePanel) removeNode(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes = slices.DeleteFunc(p.nodes, func(n int) bool { return n == id })
}

func (p *machinePanel) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("X-Server-Token") != p.key {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error_code":"node_unauthorized"}`))
		return
	}
	if r.URL.Path == "/api/v1/server/UniProxy/machine" {
		nodes := make([]map[string]any, 0, len(p.nodes))
		for _, id := range p.nodes {
			nodes = append(nodes, map[string]any{"id": id, "name": "node", "protocol": "vless"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"machine_id": 9, "name": "m", "pull_interval": 3600, "nodes": nodes})
		return
	}
	id, _ := strconv.Atoi(r.URL.Query().Get("node_id"))
	if !slices.Contains(p.nodes, id) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error_code":"node_unauthorized"}`))
		return
	}
	switch r.URL.Path {
	case "/api/v2/server/config":
		_ = json.NewEncoder(w).Encode(p.configs[id])
	case "/api/v1/server/UniProxy/user":
		users, ok := p.users[id]
		if !ok {
			users = testUsers(1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"users": users})
	case "/api/v1/server/UniProxy/alivelist":
		_, _ = w.Write([]byte(`{"alive":{}}`))
	case "/api/v1/server/UniProxy/alive", "/api/v1/server/UniProxy/push":
		_, _ = w.Write([]byte(`{"data":true}`))
	default:
		http.NotFound(w, r)
	}
}

func testMachine(t *testing.T, url string) *Machine {
	t.Helper()
	retry := 0
	m, err := NewMachine(conf.MachineConfig{APIHost: url, Key: testMachineKey, Timeout: 5, RetryCount: &retry, Protocol: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func nodeIDs(configs []conf.NodeConfig) []int {
	ids := make([]int, 0, len(configs))
	for _, c := range configs {
		ids = append(ids, c.NodeID)
	}
	return ids
}

func TestMachineFollowsThePanelsNodeList(t *testing.T) {
	p := newMachinePanel(t)
	p.addNode(t, 1, nil)
	p.addNode(t, 2, nil)
	m := testMachine(t, p.URL)
	if len(m.NodeConfigs()) != 0 {
		t.Fatal("a machine runs no node before its list arrives")
	}
	changed, err := m.Refresh(t.Context())
	if err != nil || !changed {
		t.Fatalf("first list: changed=%v err=%v", changed, err)
	}
	if got := nodeIDs(m.NodeConfigs()); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("machine nodes = %v", got)
	}
	if changed, _ := m.Refresh(t.Context()); changed {
		t.Fatal("an unchanged list reported as changed")
	}
	p.removeNode(1)
	if changed, _ := m.Refresh(t.Context()); !changed {
		t.Fatal("a removed node was not noticed")
	}
	// A configured node the machine runs too runs once, as the machine's.
	legacy := conf.NodeConfig{APIHost: p.URL + "/", NodeID: 2, Key: "own-key"}
	other := conf.NodeConfig{APIHost: "https://other.example", NodeID: 2, Key: "other"}
	desired := Desired([]conf.NodeConfig{legacy, other}, m)
	if len(desired) != 2 || desired[0] != other || desired[1].Key != testMachineKey || desired[1].NodeID != 2 {
		t.Fatalf("desired = %+v", desired)
	}
}

func TestMachineKeepsItsListWhenThePanelRejectsIt(t *testing.T) {
	p := newMachinePanel(t)
	p.addNode(t, 1, nil)
	m := testMachine(t, p.URL)
	if _, err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.key = "rotated"
	p.mu.Unlock()
	if _, err := m.Refresh(t.Context()); err == nil {
		t.Fatal("a rejected key must be reported")
	}
	if got := nodeIDs(m.NodeConfigs()); !slices.Equal(got, []int{1}) {
		t.Fatalf("running nodes dropped on a failed fetch: %v", got)
	}
}

func TestApplyAddsAndRemovesNodesWithoutRestartingTheCore(t *testing.T) {
	p := newMachinePanel(t)
	p.addNode(t, 1, nil)
	m := testMachine(t, p.URL)
	if _, err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	n, err := New(Desired(nil, m), nil)
	if err != nil || len(n.NodeInfos) != 1 {
		t.Fatalf("first generation: %v", err)
	}
	x, _ := startGeneration(t, n)
	server := x.Server
	first := n.controllers[0]

	p.addNode(t, 2, nil)
	if _, err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	applied, err := n.Apply(x.Config, Desired(nil, m), x)
	if err != nil || !applied {
		t.Fatalf("adding a node in place: applied=%v err=%v", applied, err)
	}
	if x.Server != server || n.controllers[0] != first || len(n.controllers) != 2 {
		t.Fatal("adding a node restarted the core or the running node")
	}
	if !inboundRunning(n.controllers[1]) || !inboundRunning(first) {
		t.Fatal("both nodes must serve after the add")
	}

	p.removeNode(1)
	if _, err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	applied, err = n.Apply(x.Config, Desired(nil, m), x)
	if err != nil || !applied {
		t.Fatalf("removing a node in place: applied=%v err=%v", applied, err)
	}
	if len(n.Configs) != 1 || n.Configs[0].NodeID != 2 || x.Server != server {
		t.Fatalf("after removal: %+v", nodeIDs(n.Configs))
	}
	if inboundRunning(first) {
		t.Fatal("the removed node still serves")
	}
}

func TestApplyLeavesRouteChangesToAFullReload(t *testing.T) {
	p := newMachinePanel(t)
	p.addNode(t, 1, nil)
	m := testMachine(t, p.URL)
	if _, err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	n, err := New(Desired(nil, m), nil)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := startGeneration(t, n)
	p.addNode(t, 2, map[string]any{
		"routes": []map[string]any{{"id": 1, "rule": json.RawMessage(`{"outboundTag":"block","domain":["example.com"]}`)}},
	})
	if _, err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	applied, err := n.Apply(x.Config, Desired(nil, m), x)
	if err != nil || applied {
		t.Fatalf("a node with routes needs a new core: applied=%v err=%v", applied, err)
	}
	if len(n.Configs) != 1 {
		t.Fatal("a declined apply must not change the running nodes")
	}
}

func TestSameConfigIgnoresEncodingOrder(t *testing.T) {
	p := newMachinePanel(t)
	p.addNode(t, 1, nil)
	m := testMachine(t, p.URL)
	if _, err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	n, err := New(Desired(nil, m), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := conf.New()
	for range 5 {
		same, err := core.SameConfig(c, n.NodeInfos, n.NodeInfos)
		if err != nil || !same {
			t.Fatalf("the same nodes must build the same core config: %v %v", same, err)
		}
	}
}
