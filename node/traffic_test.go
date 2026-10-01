package node

import (
	"net/http"
	"testing"

	panel "github.com/youtonghy/TJXT_node/api/v2board"
	"github.com/youtonghy/TJXT_node/common/format"
)

func TestTrafficBatchRetriedWithSameIDUntilAcked(t *testing.T) {
	p := newFakePanel(t, "vless", testUsers(1))
	ctrl, x := startTestController(t, p)
	counter := x.TrafficCounter(ctrl.tag)
	email := format.UserTag(ctrl.tag, testUsers(1)[0].Uuid)
	counter.Tx(email, 1000)
	counter.Rx(email, 2000)

	p.setPushStatus(http.StatusServiceUnavailable)
	if _, err := ctrl.flushTraffic(t.Context(), 0); err == nil {
		t.Fatal("expected the first push to fail")
	}
	// Bytes counted after the failed push must not join the pending batch.
	counter.Tx(email, 5)
	p.setPushStatus(http.StatusOK)
	if _, err := ctrl.flushTraffic(t.Context(), 0); err != nil {
		t.Fatalf("retry: %v", err)
	}
	pushes := p.Pushes()
	if len(pushes) != 2 {
		t.Fatalf("expected 2 pushes, got %d", len(pushes))
	}
	if pushes[0].BatchID == "" || pushes[0].BatchID != pushes[1].BatchID || pushes[1].Header != pushes[1].BatchID {
		t.Fatalf("retry did not reuse the batch ID: %+v", pushes)
	}
	if got := pushes[1].Traffic["1"]; got != [2]int64{1000, 2000} {
		t.Fatalf("retry changed the batch content: %+v", got)
	}
	if up, down := counter.GetUpCount(email), counter.GetDownCount(email); up != 5 || down != 0 {
		t.Fatalf("ack subtracted the wrong amount: up=%d down=%d", up, down)
	}
	// The next report is a fresh batch with the remaining bytes.
	if _, err := ctrl.flushTraffic(t.Context(), 0); err != nil {
		t.Fatalf("next push: %v", err)
	}
	pushes = p.Pushes()
	if len(pushes) != 3 || pushes[2].BatchID == pushes[1].BatchID || pushes[2].Traffic["1"] != [2]int64{5, 0} {
		t.Fatalf("unexpected third push %+v", pushes)
	}
}

func TestCloseFlushesRemainingTraffic(t *testing.T) {
	p := newFakePanel(t, "vless", testUsers(1))
	ctrl, x := startTestController(t, p)
	email := format.UserTag(ctrl.tag, testUsers(1)[0].Uuid)
	x.TrafficCounter(ctrl.tag).Tx(email, 42)
	ctrl.flushAll("test")
	pushes := p.Pushes()
	if len(pushes) != 1 || pushes[0].Traffic["1"] != [2]int64{42, 0} {
		t.Fatalf("final flush did not report: %+v", pushes)
	}
}

func TestPermanentlyRejectedBatchIsDropped(t *testing.T) {
	p := newFakePanel(t, "vless", testUsers(1))
	ctrl, x := startTestController(t, p)
	email := format.UserTag(ctrl.tag, testUsers(1)[0].Uuid)
	counter := x.TrafficCounter(ctrl.tag)
	counter.Tx(email, 10)

	p.setPushStatus(http.StatusServiceUnavailable)
	for range maxBatchRejects + 1 {
		if _, err := ctrl.flushTraffic(t.Context(), 0); err == nil {
			t.Fatal("expected push error")
		}
	}
	if ctrl.pendingTraffic == nil {
		t.Fatal("a retryable failure must keep the batch")
	}

	p.setPushStatus(http.StatusUnprocessableEntity)
	for range maxBatchRejects {
		if _, err := ctrl.flushTraffic(t.Context(), 0); err == nil {
			t.Fatal("expected push error")
		}
	}
	if ctrl.pendingTraffic != nil || counter.GetUpCount(email) != 0 {
		t.Fatalf("rejected batch kept: pending=%v up=%d", ctrl.pendingTraffic != nil, counter.GetUpCount(email))
	}

	// Later traffic is reported again.
	p.setPushStatus(http.StatusOK)
	counter.Tx(email, 3)
	if _, err := ctrl.flushTraffic(t.Context(), 0); err != nil {
		t.Fatalf("push after drop: %v", err)
	}
}

func TestRemovingUsersReportsTheirTrafficFirst(t *testing.T) {
	p := newFakePanel(t, "vless", testUsers(1, 2))
	ctrl, x := startTestController(t, p)
	x.TrafficCounter(ctrl.tag).Tx(format.UserTag(ctrl.tag, testUsers(1)[0].Uuid), 64)
	// A transient empty list must not lose the users' unreported traffic.
	p.setUsers([]panel.UserInfo{})
	poll(t, ctrl)
	pushes := p.Pushes()
	if len(pushes) != 1 || pushes[0].Traffic["1"] != [2]int64{64, 0} {
		t.Fatalf("traffic of removed users was not reported: %+v", pushes)
	}
}

// TestPollAndReportConcurrently runs the two controller tasks in parallel with
// live traffic and user churn; run it with -race.
func TestPollAndReportConcurrently(t *testing.T) {
	p := newFakePanel(t, "vless", testUsers(1, 2))
	ctrl, x := startTestController(t, p)
	counter := x.TrafficCounter(ctrl.tag)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 20 {
			if i%2 == 0 {
				p.setUsers(testUsers(1, 3))
			} else {
				p.setUsers(testUsers(1, 2))
			}
			_ = ctrl.nodeInfoMonitor(t.Context())
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
		}
		counter.Tx(format.UserTag(ctrl.tag, testUsers(1)[0].Uuid), 10)
		_ = ctrl.reportUserTrafficTask(t.Context())
	}
}
