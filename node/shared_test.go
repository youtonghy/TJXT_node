package node

import (
	"context"
	"testing"

	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/common/format"
	"github.com/youtonghy/TJXT_node/core"
)

// hasUser reports whether the inbound tagged inbound has the user of node tag
// with the given credential.
func hasUser(t *testing.T, x *core.V2Core, inbound, tag, uuid string) bool {
	t.Helper()
	man, err := x.GetUserManager(inbound)
	if err != nil {
		return false
	}
	return man.GetUser(context.Background(), format.UserTag(tag, uuid)) != nil
}

func TestLinesShareTheInboundOfTheirNode(t *testing.T) {
	p := newMachinePanel(t)
	p.addNode(t, 1, nil)
	port := p.configs[1]["server_port"]
	p.addNode(t, 2, map[string]any{"server_port": port, "shared_inbound": 1})
	p.users[2] = testUsers(2)
	m := testMachine(t, p.URL)
	if _, err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	n, err := New(Desired(nil, m), nil)
	if err != nil || len(n.controllers) != 2 {
		t.Fatalf("both nodes must run: %v %d", err, len(n.controllers))
	}
	x, _ := startGeneration(t, n)
	entry, line := n.controllers[0], n.controllers[1]
	if !entry.sharesInbound || !line.sharesInbound || line.info.Inbound() != entry.tag {
		t.Fatalf("shared inbound not recognised: entry=%v line=%v inbound=%s", entry.sharesInbound, line.sharesInbound, line.info.Inbound())
	}
	entryUser, lineUser := testUsers(1)[0].Uuid, testUsers(2)[0].Uuid
	if !hasUser(t, x, entry.tag, entry.tag, entryUser) || !hasUser(t, x, entry.tag, line.tag, lineUser) {
		t.Fatal("the shared inbound must hold the users of both nodes, each with its own node tag")
	}
	if !inboundRunning(entry) {
		t.Fatal("the shared inbound does not listen")
	}

	// Rebuilding the inbound with new settings keeps the line's users.
	entry.reloadInbound(entry.nodeInfo())
	if !hasUser(t, x, entry.tag, line.tag, lineUser) || !hasUser(t, x, entry.tag, entry.tag, entryUser) {
		t.Fatal("a rebuilt shared inbound lost users")
	}

	// A line's user changes touch only its own users.
	p.mu.Lock()
	p.users[2] = testUsers(3)
	p.mu.Unlock()
	poll(t, line)
	if hasUser(t, x, entry.tag, line.tag, lineUser) || !hasUser(t, x, entry.tag, line.tag, testUsers(3)[0].Uuid) ||
		!hasUser(t, x, entry.tag, entry.tag, entryUser) {
		t.Fatal("line user change not applied on its own")
	}

	// The inbound runs while any node has users: the line alone keeps it up.
	p.mu.Lock()
	p.users[1] = []panel.UserInfo{}
	p.mu.Unlock()
	poll(t, entry)
	if !inboundRunning(entry) || !hasUser(t, x, entry.tag, line.tag, testUsers(3)[0].Uuid) {
		t.Fatal("the line's users must keep the shared inbound running")
	}
	p.mu.Lock()
	p.users[2] = []panel.UserInfo{}
	p.mu.Unlock()
	poll(t, line)
	if inboundRunning(entry) {
		t.Fatal("the shared inbound must stop once no node has users")
	}
}

func TestNodesOnOnePortWithoutSharingRunOnlyTheOldest(t *testing.T) {
	p := newMachinePanel(t)
	p.addNode(t, 1, nil)
	p.addNode(t, 3, map[string]any{"server_port": p.configs[1]["server_port"]})
	p.addNode(t, 2, nil)
	m := testMachine(t, p.URL)
	if _, err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	n, err := New(Desired(nil, m), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := nodeIDs(n.Configs); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("running nodes = %v, want 1 and 2 (3 conflicts with 1)", got)
	}
}
