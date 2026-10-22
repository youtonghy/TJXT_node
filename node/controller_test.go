package node

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/conf"
)

func inboundRunning(ctrl *Controller) bool {
	port := ctrl.info.Common.ServerPort
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func poll(t *testing.T, ctrl *Controller) {
	t.Helper()
	if err := ctrl.nodeInfoMonitor(t.Context()); err != nil {
		t.Fatalf("poll: %v", err)
	}
}

func TestUserLifecycle(t *testing.T) {
	for _, protocol := range []string{"vless", "mixed"} {
		t.Run(protocol, func(t *testing.T) {
			p := newFakePanel(t, protocol, []panel.UserInfo{})
			ctrl, x := startTestController(t, p)
			if inboundRunning(ctrl) {
				t.Fatal("inbound must stay down with zero users")
			}

			// 0 -> N starts the inbound.
			p.setUsers(testUsers(1, 2))
			poll(t, ctrl)
			if !inboundRunning(ctrl) || len(ctrl.userList) != 2 {
				t.Fatalf("0->N: running=%v users=%d", inboundRunning(ctrl), len(ctrl.userList))
			}

			// Unchanged list (304) keeps everything as is.
			notModified := p.NotModified()
			poll(t, ctrl)
			if p.NotModified() != notModified+1 || !inboundRunning(ctrl) || len(ctrl.userList) != 2 {
				t.Fatal("304 must keep the applied users")
			}

			// N -> M updates users in place (or rebuilds for mixed).
			p.setUsers(testUsers(2, 3, 4))
			poll(t, ctrl)
			if !inboundRunning(ctrl) || len(ctrl.userList) != 3 {
				t.Fatalf("N->M: running=%v users=%d", inboundRunning(ctrl), len(ctrl.userList))
			}

			// N -> 0 stops the inbound.
			p.setUsers([]panel.UserInfo{})
			poll(t, ctrl)
			if inboundRunning(ctrl) || len(ctrl.userList) != 0 {
				t.Fatalf("N->0: running=%v users=%d", inboundRunning(ctrl), len(ctrl.userList))
			}
			if _, err := x.GetUserManager(ctrl.tag); err == nil {
				t.Fatal("inbound handler still registered after N->0")
			}

			// 0 -> N again brings it back.
			p.setUsers(testUsers(5))
			poll(t, ctrl)
			if !inboundRunning(ctrl) || len(ctrl.userList) != 1 {
				t.Fatalf("0->N again: running=%v users=%d", inboundRunning(ctrl), len(ctrl.userList))
			}
		})
	}
}

func TestCloseIsIdempotentWhenInboundIsDown(t *testing.T) {
	p := newFakePanel(t, "vless", []panel.UserInfo{})
	ctrl, x := startTestController(t, p)
	if err := x.DelNode(ctrl.tag); err != nil {
		t.Fatalf("DelNode on a missing inbound: %v", err)
	}
	if err := ctrl.Close(); err != nil {
		t.Fatalf("close with inbound down: %v", err)
	}
}

func TestBrokenNodeConfigDoesNotFailStart(t *testing.T) {
	p := newFakePanel(t, "shadowsocks", testUsers(1))
	p.config["cipher"] = "no-such-cipher"
	ctrl, _ := startTestController(t, p)
	if inboundRunning(ctrl) || !ctrl.resync {
		t.Fatalf("broken inbound: running=%v resync=%v", inboundRunning(ctrl), ctrl.resync)
	}
	// The next poll retries and keeps failing without stopping the controller.
	poll(t, ctrl)
	if inboundRunning(ctrl) || !ctrl.resync {
		t.Fatal("a broken inbound must keep retrying")
	}
}

func TestNewNodesDoNotSeeTheirOwnConfigAsChanged(t *testing.T) {
	p := newFakePanel(t, "vless", testUsers(1))
	retry := 0
	n, err := New([]conf.NodeConfig{{APIHost: p.URL, NodeID: 1, Key: "k", Timeout: 5, RetryCount: &retry, Protocol: "v1"}}, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// A fresh client would report the unchanged config as new and reload forever.
	info, err := n.controllers[0].apiClient.GetNodeInfo(t.Context())
	if err != nil || info != nil {
		t.Fatalf("unchanged config reported as new: info=%v err=%v", info != nil, err)
	}
}

// certBlockedPanel serves a self-signed TLS vless node whose cert directory
// cannot be created until the returned function removes the blocking file.
func certBlockedPanel(t *testing.T) (*fakePanel, func()) {
	t.Helper()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "certs")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newFakePanel(t, "vless", testUsers(1))
	p.config["tls"] = 1
	p.config["tls_settings"] = map[string]any{
		"cert_mode":   "self",
		"server_name": "node.test",
		"cert_file":   filepath.Join(blocker, "node.cer"),
		"key_file":    filepath.Join(blocker, "node.key"),
	}
	return p, func() {
		if err := os.Remove(blocker); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCertFailureKeepsOnlyThisNodeDownUntilAPollFixesIt(t *testing.T) {
	p, unblock := certBlockedPanel(t)
	ctrl, _ := startTestController(t, p)
	if inboundRunning(ctrl) || !ctrl.certPending {
		t.Fatalf("missing cert: running=%v certPending=%v", inboundRunning(ctrl), ctrl.certPending)
	}
	poll(t, ctrl)
	if inboundRunning(ctrl) || !ctrl.certPending {
		t.Fatal("the cert must keep failing while its directory cannot be created")
	}
	unblock()
	// The user list is unchanged (304), but the poll still retries the cert
	// and then builds the inbound.
	poll(t, ctrl)
	if ctrl.certPending || !inboundRunning(ctrl) {
		t.Fatalf("after the fix: running=%v certPending=%v", inboundRunning(ctrl), ctrl.certPending)
	}
}
