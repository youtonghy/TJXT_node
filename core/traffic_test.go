package core

import (
	"sync"
	"testing"

	"github.com/youtonghy/TJXT_node/common/counter"
	"github.com/youtonghy/TJXT_node/common/format"
	"github.com/youtonghy/TJXT_node/core/app/dispatcher"
)

const testTag = "[panel]-vless:1"

func newTrafficTestCore(uids map[string]int) (*V2Core, *counter.TrafficCounter) {
	v := &V2Core{
		users:      &UserMap{uidMap: make(map[string]int)},
		dispatcher: &dispatcher.DefaultDispatcher{},
	}
	for uuid, id := range uids {
		v.users.uidMap[format.UserTag(testTag, uuid)] = id
		v.users.uidMap[uuid] = id
	}
	c := counter.NewTrafficCounter()
	v.dispatcher.Counter.Store(testTag, c)
	return v, c
}

func TestSnapshotDoesNotResetAndAckKeepsNewBytes(t *testing.T) {
	v, c := newTrafficTestCore(map[string]int{"u1": 1})
	email := format.UserTag(testTag, "u1")
	c.Tx(email, 100)
	c.Rx(email, 200)

	batch := v.SnapshotTraffic(testTag, 0)
	if batch == nil || batch.ID == "" || len(batch.Entries) != 1 {
		t.Fatalf("unexpected batch %+v", batch)
	}
	if got := c.GetUpCount(email); got != 100 {
		t.Fatalf("snapshot reset the counter: up=%d", got)
	}
	// Traffic counted while the batch is in flight must survive the ack.
	c.Tx(email, 7)
	c.Rx(email, 9)
	v.AckTraffic(batch)
	if up, down := c.GetUpCount(email), c.GetDownCount(email); up != 7 || down != 9 {
		t.Fatalf("ack lost concurrent bytes: up=%d down=%d", up, down)
	}
	traffic := batch.Traffic()
	if len(traffic) != 1 || traffic[0].UID != 1 || traffic[0].Upload != 100 || traffic[0].Download != 200 {
		t.Fatalf("unexpected traffic %+v", traffic)
	}
}

func TestSnapshotDropsRemovedUsersAndKeepsSmallCounters(t *testing.T) {
	v, c := newTrafficTestCore(map[string]int{"u1": 1})
	c.Tx(format.UserTag(testTag, "ghost"), 5000)
	c.Tx(format.UserTag(testTag, "u1"), 10)

	if batch := v.SnapshotTraffic(testTag, 1); batch != nil {
		t.Fatalf("expected no batch below threshold and for removed users, got %+v", batch)
	}
	if got := c.GetUpCount(format.UserTag(testTag, "ghost")); got != 0 {
		t.Fatalf("counter of a removed user was kept: %d", got)
	}
	if got := c.GetUpCount(format.UserTag(testTag, "u1")); got != 10 {
		t.Fatalf("small counter must keep accumulating: %d", got)
	}
}

func TestTrafficMergesEntriesOfOneUser(t *testing.T) {
	b := &TrafficBatch{Entries: []TrafficEntry{
		{Email: "a", UID: 1, Upload: 1, Download: 2},
		{Email: "b", UID: 1, Upload: 3, Download: 4},
		{Email: "c", UID: 2, Upload: 5, Download: 6},
	}}
	traffic := b.Traffic()
	if len(traffic) != 2 || traffic[0].Upload != 4 || traffic[0].Download != 6 || traffic[1].UID != 2 {
		t.Fatalf("unexpected merge %+v", traffic)
	}
}

func TestSnapshotAckUnderConcurrentWrites(t *testing.T) {
	v, c := newTrafficTestCore(map[string]int{"u1": 1})
	email := format.UserTag(testTag, "u1")
	const writers, perWriter = 8, 10000
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWriter {
				c.Tx(email, 1)
			}
		}()
	}
	var reported int64
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	for finished := false; !finished; {
		select {
		case <-done:
			finished = true
		default:
		}
		if b := v.SnapshotTraffic(testTag, 0); b != nil {
			for _, tr := range b.Traffic() {
				reported += tr.Upload
			}
			v.AckTraffic(b)
		}
	}
	reported += c.GetUpCount(email)
	if reported != writers*perWriter {
		t.Fatalf("lost traffic: reported %d of %d", reported, writers*perWriter)
	}
}
