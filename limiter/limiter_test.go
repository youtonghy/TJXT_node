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
