package core

import (
	"sort"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	panel "github.com/youtonghy/TJXT_node/api/v2board"
	"github.com/youtonghy/TJXT_node/common/counter"
)

// maxLoggedUnresolved caps how many unresolved counter keys one snapshot logs.
const maxLoggedUnresolved = 5

// TrafficEntry is the traffic of one counter key (user email) captured in a batch.
type TrafficEntry struct {
	Email    string
	UID      int
	Upload   int64
	Download int64
	storage  *counter.TrafficStorage
}

// TrafficBatch is a snapshot of per-user traffic that is reported to the panel
// under a stable ID. The counters are only decremented once the panel acknowledges
// the batch, so a failed report is retried with the same ID and the same content.
type TrafficBatch struct {
	ID      string
	Entries []TrafficEntry
}

// Traffic merges the batch entries per UID in the shape the panel client sends.
func (b *TrafficBatch) Traffic() []panel.UserTraffic {
	if b == nil {
		return nil
	}
	index := make(map[int]int, len(b.Entries))
	traffic := make([]panel.UserTraffic, 0, len(b.Entries))
	for _, e := range b.Entries {
		if i, ok := index[e.UID]; ok {
			traffic[i].Upload += e.Upload
			traffic[i].Download += e.Download
			continue
		}
		index[e.UID] = len(traffic)
		traffic = append(traffic, panel.UserTraffic{UID: e.UID, Upload: e.Upload, Download: e.Download})
	}
	return traffic
}

// SnapshotTraffic captures the current counters of an inbound without resetting
// them. Counters at or below minTraffic KB are left to accumulate. Counters whose
// key does not resolve to a user ID are kept (and logged) until the user resolves
// or is removed. It returns nil when there is nothing to report.
func (vc *V2Core) SnapshotTraffic(tag string, minTraffic int) *TrafficBatch {
	if vc.dispatcher == nil {
		return nil
	}
	v, ok := vc.dispatcher.Counter.Load(tag)
	if !ok {
		return nil
	}
	c := v.(*counter.TrafficCounter)
	threshold := int64(minTraffic) * 1000
	var entries []TrafficEntry
	var unresolved []string
	vc.users.mapLock.RLock()
	c.Counters.Range(func(key, value any) bool {
		email := key.(string)
		storage := value.(*counter.TrafficStorage)
		up := storage.UpCounter.Load()
		down := storage.DownCounter.Load()
		if up+down <= threshold || up+down <= 0 {
			return true
		}
		uid := resolveUID(vc.users.uidMap, email)
		if uid == 0 {
			unresolved = append(unresolved, email)
			return true
		}
		entries = append(entries, TrafficEntry{
			Email:    email,
			UID:      uid,
			Upload:   up,
			Download: down,
			storage:  storage,
		})
		return true
	})
	vc.users.mapLock.RUnlock()
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		logged := unresolved
		if len(logged) > maxLoggedUnresolved {
			logged = logged[:maxLoggedUnresolved]
		}
		log.WithFields(log.Fields{
			"tag":   tag,
			"count": len(unresolved),
			"keys":  logged,
		}).Warn("Traffic counters without a known user ID are kept until the user resolves")
	}
	if len(entries) == 0 {
		return nil
	}
	return &TrafficBatch{ID: uuid.NewString(), Entries: entries}
}

// AckTraffic subtracts an acknowledged batch from the live counters. Bytes counted
// after the snapshot are preserved because the subtraction is atomic.
func (vc *V2Core) AckTraffic(b *TrafficBatch) {
	if b == nil {
		return
	}
	for _, e := range b.Entries {
		if e.storage == nil {
			continue
		}
		e.storage.UpCounter.Add(-e.Upload)
		e.storage.DownCounter.Add(-e.Download)
	}
}

// TrafficCounter returns the live traffic counter of an inbound, creating it if
// needed. It returns nil before the core has started.
func (vc *V2Core) TrafficCounter(tag string) *counter.TrafficCounter {
	if vc.dispatcher == nil {
		return nil
	}
	c, _ := vc.dispatcher.Counter.LoadOrStore(tag, counter.NewTrafficCounter())
	return c.(*counter.TrafficCounter)
}
