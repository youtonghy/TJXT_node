package limiter

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/youtonghy/TJXT_node/api/panel"
)

// activityTTL is how long an (user, IP) pair without traffic stays known; well
// beyond a report interval so a device that pauses briefly is not new again.
const activityTTL = 5 * time.Minute

// nowUnix is the clock of activity tracking; tests replace it.
var nowUnix = func() int64 { return time.Now().Unix() }

// activityKey identifies a device of a user on one inbound.
type activityKey struct {
	uid int
	ip  string
}

// Activity is the last time a device of a user moved data. Connections refresh
// it on every read and write, so a long-lived connection keeps its device online
// for as long as it carries traffic, whatever report window it started in.
type Activity struct {
	last atomic.Int64
}

// Touch records traffic now.
func (a *Activity) Touch() {
	now := nowUnix()
	if a.last.Load() != now {
		a.last.Store(now)
	}
}

// activityMap is the devices of one inbound.
type activityMap struct {
	mu      sync.Mutex
	devices map[activityKey]*Activity
}

func (m *activityMap) get(key activityKey) *Activity {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.devices == nil {
		m.devices = make(map[activityKey]*Activity)
	}
	a, ok := m.devices[key]
	if !ok {
		a = &Activity{}
		m.devices[key] = a
	}
	return a
}

// activeSince reports whether the device moved data at or after since.
func (m *activityMap) activeSince(key activityKey, since int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.devices[key]
	return ok && a.last.Load() >= since
}

// collect returns the devices active after since (the previous report) and
// forgets the ones idle for longer than activityTTL.
func (m *activityMap) collect(since int64) []panel.OnlineUser {
	expired := nowUnix() - int64(activityTTL/time.Second)
	m.mu.Lock()
	defer m.mu.Unlock()
	var online []panel.OnlineUser
	for key, a := range m.devices {
		last := a.last.Load()
		if last < expired {
			delete(m.devices, key)
			continue
		}
		if last > since {
			online = append(online, panel.OnlineUser{UID: key.uid, IP: key.ip})
		}
	}
	return online
}

// Activity returns the activity record of a user's device on this inbound and
// marks it active now; nil for an unknown user. The dispatcher keeps it on the
// connection and touches it as data flows.
func (l *Limiter) Activity(taguuid, ip string, noUDPsource bool) *Activity {
	if !l.tracksDevices(noUDPsource) {
		return nil
	}
	u, ok := loadUserLimit(l, taguuid)
	if !ok {
		return nil
	}
	a := l.activity.get(activityKey{uid: u.UID, ip: normalizeIP(ip)})
	a.Touch()
	return a
}

// recentlyActive reports whether the user's device moved data on any inbound of
// this process within activityTTL: such an IP is part of the panel's device
// count already, so it is not a new device.
func recentlyActive(uid int, ip string) bool {
	since := nowUnix() - int64(activityTTL/time.Second)
	key := activityKey{uid: uid, ip: ip}
	limitLock.RLock()
	defer limitLock.RUnlock()
	for _, l := range limiter {
		if l.activity.activeSince(key, since) {
			return true
		}
	}
	return false
}
