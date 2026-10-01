package node

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/youtonghy/TJXT_node/api/panel"
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
