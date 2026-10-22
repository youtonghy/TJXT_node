package node

import (
	"testing"
	"time"

	"github.com/youtonghy/TJXT_node/conf"
	"github.com/youtonghy/TJXT_node/core"
	"github.com/youtonghy/TJXT_node/limiter"
)

func testNodeConfig(url string, nodeID int) conf.NodeConfig {
	retry := 0
	return conf.NodeConfig{APIHost: url, NodeID: nodeID, Key: "k", Timeout: 5, RetryCount: &retry, Protocol: "v1"}
}

func fastRetries(t *testing.T) {
	t.Helper()
	oldMin, oldMax := retryBackoffMin, retryBackoffMax
	retryBackoffMin, retryBackoffMax = 10*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { retryBackoffMin, retryBackoffMax = oldMin, oldMax })
}

// startGeneration starts a core and the nodes of n like the server does.
func startGeneration(t *testing.T, n *Node) (*core.V2Core, chan struct{}) {
	t.Helper()
	limiter.Init()
	c := conf.New()
	c.LogConfig.Level = "error"
	x := core.New(c)
	reload := make(chan struct{}, 1)
	x.ReloadCh = reload
	if err := x.Start(n.NodeInfos); err != nil {
		t.Fatalf("start core: %v", err)
	}
	if err := n.Start(x); err != nil {
		_ = x.Close()
		t.Fatalf("start nodes: %v", err)
	}
	t.Cleanup(func() {
		if err := n.Close(); err != nil {
			t.Errorf("close nodes: %v", err)
		}
		if err := x.Close(); err != nil {
			t.Errorf("close core: %v", err)
		}
	})
	return x, reload
}

func waitReload(t *testing.T, reload chan struct{}) {
	t.Helper()
	select {
	case <-reload:
	case <-time.After(5 * time.Second):
		t.Fatal("no reload after the panel came back")
	}
}

func TestStartupRetriesUntilPanelComesBack(t *testing.T) {
	fastRetries(t)
	p := newFakePanel(t, "vless", testUsers(1))
	p.setDown(true)
	nodes := []conf.NodeConfig{testNodeConfig(p.URL, 1)}
	n, err := New(nodes, nil)
	if err != nil {
		t.Fatalf("an unreachable panel must not fail New: %v", err)
	}
	if len(n.NodeInfos) != 0 || n.Pending() != 1 {
		t.Fatalf("want 1 pending node, got %d running %d pending", len(n.NodeInfos), n.Pending())
	}
	// Nothing runs yet, but the process (core) is up and retrying.
	_, reload := startGeneration(t, n)
	time.Sleep(50 * time.Millisecond)
	p.setDown(false)
	waitReload(t, reload)

	fetches := p.ConfigFetches()
	next, err := New(nodes, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.NodeInfos) != 1 || next.Pending() != 0 || next.NodeInfos[0].Id != 1 {
		t.Fatalf("recovered node not taken over: running=%d pending=%d", len(next.NodeInfos), next.Pending())
	}
	if p.ConfigFetches() != fetches {
		t.Fatal("the next generation fetched the recovered config again")
	}
	// The taken-over client holds the config's hash: no reload loop.
	if info, err := next.controllers[0].apiClient.GetNodeInfo(t.Context()); err != nil || info != nil {
		t.Fatalf("recovered config reported as new: info=%v err=%v", info != nil, err)
	}
}

func TestFailingNodeDoesNotBlockOthers(t *testing.T) {
	fastRetries(t)
	good := newFakePanel(t, "vless", testUsers(1))
	bad := newFakePanel(t, "vless", testUsers(1))
	bad.setDown(true)
	n, err := New([]conf.NodeConfig{testNodeConfig(bad.URL, 1), testNodeConfig(good.URL, 2)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Configs) != 1 || n.Configs[0].APIHost != good.URL || n.Pending() != 1 {
		t.Fatalf("want the good node running and the bad one pending: %+v pending=%d", n.Configs, n.Pending())
	}
	startGeneration(t, n)
	if !inboundRunning(n.controllers[0]) {
		t.Fatal("the good node did not start")
	}
}

func TestReloadKeepsRunningNodeWhenPanelIsDown(t *testing.T) {
	fastRetries(t)
	p := newFakePanel(t, "vless", testUsers(1))
	nodes := []conf.NodeConfig{testNodeConfig(p.URL, 1)}
	prev, err := New(nodes, nil)
	if err != nil || len(prev.NodeInfos) != 1 {
		t.Fatalf("first generation: %v", err)
	}
	p.setDown(true)
	next, err := New(nodes, prev)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.NodeInfos) != 1 || next.NodeInfos[0] != prev.NodeInfos[0] || next.Pending() != 0 {
		t.Fatalf("a running node must keep its config while the panel is down: running=%d pending=%d", len(next.NodeInfos), next.Pending())
	}
}

func TestPairByKeyNodeStarts(t *testing.T) {
	p := newFakePanel(t, "vless", testUsers(1))
	p.config["base_config"] = map[string]any{"push_interval": 3600, "pull_interval": 3600, "node_id": 77}
	n, err := New([]conf.NodeConfig{testNodeConfig(p.URL, 0)}, nil)
	if err != nil || len(n.NodeInfos) != 1 {
		t.Fatalf("key-only node: %v pending=%d", err, n.Pending())
	}
	if n.NodeInfos[0].Id != 77 {
		t.Fatalf("learned id = %d", n.NodeInfos[0].Id)
	}
	startGeneration(t, n)
	if !inboundRunning(n.controllers[0]) {
		t.Fatal("key-only node did not start")
	}
}

func TestGlobalTokenWithoutNodeIDStaysPending(t *testing.T) {
	fastRetries(t)
	p := newFakePanel(t, "vless", testUsers(1))
	n, err := New([]conf.NodeConfig{testNodeConfig(p.URL, 0)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.NodeInfos) != 0 || n.Pending() != 1 {
		t.Fatalf("a global token without NodeID must not start a node: running=%d", len(n.NodeInfos))
	}
}

func TestCertFailureDoesNotBlockOtherNodes(t *testing.T) {
	broken, _ := certBlockedPanel(t)
	healthy := newFakePanel(t, "vless", testUsers(1))
	n, err := New([]conf.NodeConfig{testNodeConfig(broken.URL, 1), testNodeConfig(healthy.URL, 2)}, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	startGeneration(t, n)
	var running, down int
	for _, c := range n.controllers {
		if inboundRunning(c) {
			running++
		} else {
			down++
		}
	}
	if running != 1 || down != 1 {
		t.Fatalf("want the healthy node up and the cert-less one down, got %d up %d down", running, down)
	}
}
