package limiter

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/common/format"
)

const testTag = "[panel]-vless:1"

func users(from, to int, speed int) []panel.UserInfo {
	var list []panel.UserInfo
	for i := from; i < to; i++ {
		list = append(list, panel.UserInfo{Id: i, Uuid: fmt.Sprintf("uuid-%d", i), SpeedLimit: speed, DeviceLimit: 2})
	}
	return list
}

func TestCheckLimitDeviceLimitAndUnknownUser(t *testing.T) {
	Init()
	l := AddLimiter("vless", testTag, users(1, 2, 0), map[int]int{1: 2})
	if _, reject := l.CheckLimit(format.UserTag(testTag, "uuid-1"), "1.1.1.1", true); !reject {
		t.Fatal("a user at the panel-wide device limit must be rejected on a new IP")
	}
	l.SetAliveList(map[int]int{1: 0})
	if _, reject := l.CheckLimit(format.UserTag(testTag, "uuid-1"), "1.1.1.1", true); reject {
		t.Fatal("a user below the device limit must be accepted")
	}
	if _, reject := l.CheckLimit(format.UserTag(testTag, "nobody"), "1.1.1.1", true); !reject {
		t.Fatal("unknown users must be rejected")
	}
}

func TestExpiredDynamicLimitFallsBackWithoutMutation(t *testing.T) {
	Init()
	l := AddLimiter("vless", testTag, users(1, 2, 0), nil)
	if err := l.UpdateDynamicSpeedLimit(testTag, "uuid-1", 8, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	before, _ := l.UserLimitInfo.Load(format.UserTag(testTag, "uuid-1"))
	bucket, reject := l.CheckLimit(format.UserTag(testTag, "uuid-1"), "1.1.1.1", true)
	if reject || bucket != nil {
		t.Fatalf("expired dynamic limit must not apply: bucket=%v reject=%v", bucket, reject)
	}
	after, ok := l.UserLimitInfo.Load(format.UserTag(testTag, "uuid-1"))
	if !ok || before != after {
		t.Fatal("CheckLimit must not remove or replace the user's limits")
	}
}

// TestConcurrentAccess exercises every limiter entry point at once; run it
// with -race.
func TestConcurrentAccess(t *testing.T) {
	Init()
	l := AddLimiter("vless", testTag, users(0, 50, 10), map[int]int{})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	run := func(f func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
					f(i)
				}
			}
		}()
	}
	for range 4 {
		run(func(i int) {
			l.CheckLimit(format.UserTag(testTag, fmt.Sprintf("uuid-%d", i%60)), fmt.Sprintf("10.0.0.%d", i%7), i%2 == 0)
		})
	}
	run(func(i int) {
		l.UpdateUser(testTag, users(50+i%10, 51+i%10, 0), users(i%10, i%10+1, 0), users(20, 30, i%5))
		l.UpdateUser(testTag, users(i%10, i%10+1, 5), nil, nil)
	})
	run(func(i int) { l.SetAliveList(map[int]int{i % 50: i % 3}) })
	run(func(int) { _, _ = l.GetOnlineDevice() })
	run(func(i int) {
		_ = l.UpdateDynamicSpeedLimit(testTag, fmt.Sprintf("uuid-%d", 30+i%10), i%4, time.Now().Add(time.Second))
	})
	run(func(int) { _, _ = GetLimiter(testTag) })
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// An IP the user already has online on another inbound of this node is part of the
// panel's device count, so reaching the limit must not lock it out of other inbounds.
func TestDeviceLimitAcceptsAnIPOnlineOnAnotherInbound(t *testing.T) {
	Init()
	const otherTag = "[panel]-vmess:2"
	first := AddLimiter("vless", testTag, users(1, 2, 0), map[int]int{1: 0})
	second := AddLimiter("vmess", otherTag, users(1, 2, 0), map[int]int{1: 0})
	if _, reject := first.CheckLimit(format.UserTag(testTag, "uuid-1"), "1.1.1.1", true); reject {
		t.Fatal("first device must be accepted")
	}
	first.SetAliveList(map[int]int{1: 2})
	second.SetAliveList(map[int]int{1: 2})
	if _, reject := second.CheckLimit(format.UserTag(otherTag, "uuid-1"), "1.1.1.1", true); reject {
		t.Fatal("an IP online on another inbound is not a new device")
	}
	if _, reject := second.CheckLimit(format.UserTag(otherTag, "uuid-1"), "2.2.2.2", true); !reject {
		t.Fatal("a new IP at the limit must be rejected")
	}
	// After a report window the IP is only in the previous window of the first inbound.
	if _, err := first.GetOnlineDevice(); err != nil {
		t.Fatal(err)
	}
	if _, err := second.GetOnlineDevice(); err != nil {
		t.Fatal(err)
	}
	third := AddLimiter("trojan", "[panel]-trojan:3", users(1, 2, 0), map[int]int{1: 2})
	if _, reject := third.CheckLimit(format.UserTag("[panel]-trojan:3", "uuid-1"), "1.1.1.1", true); reject {
		t.Fatal("an IP from the previous window of another inbound is not a new device")
	}
}

func TestWireGuardIsNotCountedAsDevices(t *testing.T) {
	Init()
	l := AddLimiter("wireguard", "[panel]-wireguard:4", users(1, 2, 0), map[int]int{1: 2})
	if _, reject := l.CheckLimit(format.UserTag("[panel]-wireguard:4", "uuid-1"), "10.0.0.2", true); reject {
		t.Fatal("WireGuard tunnel addresses are not client devices")
	}
}

// A device whose connection keeps carrying traffic stays online across reports,
// and stays known at the device limit; an idle one is forgotten after the TTL.
func TestLongLivedConnectionsStayOnline(t *testing.T) {
	Init()
	now := int64(1_000_000)
	nowUnix = func() int64 { return now }
	defer func() { nowUnix = func() int64 { return time.Now().Unix() } }()

	l := AddLimiter("vless", testTag, users(1, 2, 0), map[int]int{1: 0})
	key := format.UserTag(testTag, "uuid-1")
	if _, reject := l.CheckLimit(key, "1.1.1.1", true); reject {
		t.Fatal("first device must be accepted")
	}
	activity := l.Activity(key, "1.1.1.1", true)
	if activity == nil {
		t.Fatal("a known user's TCP device is tracked")
	}
	online := func() []panel.OnlineUser {
		users, err := l.GetOnlineDevice()
		if err != nil {
			t.Fatal(err)
		}
		return *users
	}
	if got := online(); len(got) != 1 {
		t.Fatalf("first report: %+v", got)
	}
	// Two reports later the connection still carries traffic: still online.
	now += 60
	if got := online(); len(got) != 0 {
		t.Fatalf("idle window: %+v", got)
	}
	now += 60
	activity.Touch()
	now += 30
	if got := online(); len(got) != 1 || got[0].IP != "1.1.1.1" {
		t.Fatalf("long-lived connection dropped: %+v", got)
	}
	// At the limit the recently active IP opens another connection: not a new device.
	l.SetAliveList(map[int]int{1: 2})
	if _, reject := l.CheckLimit(key, "1.1.1.1", true); reject {
		t.Fatal("a recently active device is not new")
	}
	if _, reject := l.CheckLimit(key, "2.2.2.2", true); !reject {
		t.Fatal("a new IP at the limit must be rejected")
	}
	// Idle beyond the TTL: forgotten.
	now += int64(activityTTL/time.Second) + 1
	online()
	if recentlyActive(1, "1.1.1.1") {
		t.Fatal("an idle device is forgotten after the TTL")
	}
}

func TestUDPAndWireGuardConnectionsAreNotTracked(t *testing.T) {
	Init()
	vless := AddLimiter("vless", testTag, users(1, 2, 0), nil)
	if vless.Activity(format.UserTag(testTag, "uuid-1"), "1.1.1.1", false) != nil {
		t.Fatal("UDP sources of TCP-based inbounds are not devices")
	}
	hy2 := AddLimiter("hysteria2", "[panel]-hysteria2:5", users(1, 2, 0), nil)
	if hy2.Activity(format.UserTag("[panel]-hysteria2:5", "uuid-1"), "1.1.1.1", false) == nil {
		t.Fatal("Hysteria 2 clients are devices")
	}
	wg := AddLimiter("wireguard", "[panel]-wireguard:6", users(1, 2, 0), nil)
	if wg.Activity(format.UserTag("[panel]-wireguard:6", "uuid-1"), "10.0.0.2", true) != nil {
		t.Fatal("WireGuard tunnel addresses are not devices")
	}
}
