package limiter

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/common/format"
	"github.com/youtonghy/TJXT_node/common/rate"
)

var limitLock sync.RWMutex
var limiter map[string]*Limiter

func Init() {
	limiter = map[string]*Limiter{}
}

// Limiter holds the per-inbound speed and device limits. CheckLimit runs on
// dispatcher goroutines while the node controller updates users and alive
// counts, so read-mostly state is published as immutable snapshots:
// UserLimitInfo values are never mutated after they are stored (writers store
// a new *UserLimitInfo), and the alive list and previous online set are swapped
// atomically.
type Limiter struct {
	Nodetype      string    // Node type, e.g. "v2ray", "trojan", "shadowsocks"
	SpeedLimit    int       // Node speed limit in Mbps
	UserOnlineIP  *sync.Map // Key: TagUUID, value: {Key: Ip, value: Uid}
	UserLimitInfo *sync.Map // Key: TagUUID value: *UserLimitInfo (immutable)
	SpeedLimiter  *sync.Map // key: TagUUID, value: *DynamicBucket

	oldUserOnline atomic.Pointer[sync.Map]    // Key: Ip, value: Uid
	aliveList     atomic.Pointer[map[int]int] // Key: Uid, value: alive_ip
	// writeMu serializes writers of UserLimitInfo so read-copy-update
	// replacements do not lose each other's changes.
	writeMu sync.Mutex
}

// UserLimitInfo is an immutable snapshot of a user's limits; replace it
// instead of modifying it.
type UserLimitInfo struct {
	UID               int
	SpeedLimit        int
	DeviceLimit       int
	DynamicSpeedLimit int
	ExpireTime        int64
	OverLimit         bool
}

func AddLimiter(nodetype string, tag string, users []panel.UserInfo, aliveList map[int]int) *Limiter {
	l := &Limiter{
		Nodetype:      nodetype,
		UserOnlineIP:  new(sync.Map),
		UserLimitInfo: new(sync.Map),
		SpeedLimiter:  new(sync.Map),
	}
	l.oldUserOnline.Store(new(sync.Map))
	l.SetAliveList(aliveList)
	for i := range users {
		l.storeUserLimit(tag, users[i].Uuid, &UserLimitInfo{
			UID:         users[i].Id,
			SpeedLimit:  users[i].SpeedLimit,
			DeviceLimit: users[i].DeviceLimit,
		})
	}
	limitLock.Lock()
	limiter[tag] = l
	limitLock.Unlock()
	return l
}

// SetAliveList publishes a new panel-wide alive IP count per user.
func (l *Limiter) SetAliveList(aliveList map[int]int) {
	if aliveList == nil {
		aliveList = map[int]int{}
	}
	l.aliveList.Store(&aliveList)
}

func (l *Limiter) aliveCount(uid int) int {
	if m := l.aliveList.Load(); m != nil {
		return (*m)[uid]
	}
	return 0
}

// storeUserLimit publishes info under both the tagged and the bare UUID key.
func (l *Limiter) storeUserLimit(tag, uuid string, info *UserLimitInfo) {
	l.UserLimitInfo.Store(format.UserTag(tag, uuid), info)
	l.UserLimitInfo.Store(uuid, info)
}

func GetLimiter(tag string) (info *Limiter, err error) {
	limitLock.RLock()
	info, ok := limiter[tag]
	limitLock.RUnlock()
	if !ok {
		return nil, errors.New("not found")
	}
	return info, nil
}

func DeleteLimiter(tag string) {
	limitLock.Lock()
	delete(limiter, tag)
	limitLock.Unlock()
}

func (l *Limiter) UpdateUser(tag string, added []panel.UserInfo, deleted []panel.UserInfo, modified []panel.UserInfo) {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	for i := range deleted {
		l.UserLimitInfo.Delete(format.UserTag(tag, deleted[i].Uuid))
		l.UserLimitInfo.Delete(deleted[i].Uuid)
		l.UserOnlineIP.Delete(format.UserTag(tag, deleted[i].Uuid))
		l.UserOnlineIP.Delete(deleted[i].Uuid)
		l.SpeedLimiter.Delete(format.UserTag(tag, deleted[i].Uuid))
		l.SpeedLimiter.Delete(deleted[i].Uuid)
	}
	for i := range modified {
		if v, ok := l.UserLimitInfo.Load(format.UserTag(tag, modified[i].Uuid)); ok {
			u := *v.(*UserLimitInfo)
			u.SpeedLimit = modified[i].SpeedLimit
			u.DeviceLimit = modified[i].DeviceLimit
			l.storeUserLimit(tag, modified[i].Uuid, &u)
		}
		limit := int64(determineSpeedLimit(l.SpeedLimit, modified[i].SpeedLimit)) * 1000000 / 8
		if limit > 0 {
			if v, ok := l.SpeedLimiter.Load(format.UserTag(tag, modified[i].Uuid)); ok {
				d := v.(*rate.DynamicBucket)
				d.Update(limit)
			} else {
				d := rate.NewDynamicBucket(limit)
				l.SpeedLimiter.Store(format.UserTag(tag, modified[i].Uuid), d)
			}
		} else {
			l.SpeedLimiter.Delete(format.UserTag(tag, modified[i].Uuid))
		}
	}
	for i := range added {
		l.storeUserLimit(tag, added[i].Uuid, &UserLimitInfo{
			UID:         added[i].Id,
			SpeedLimit:  added[i].SpeedLimit,
			DeviceLimit: added[i].DeviceLimit,
		})
	}
}

func (l *Limiter) UpdateDynamicSpeedLimit(tag, uuid string, limit int, expire time.Time) error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	v, ok := l.UserLimitInfo.Load(format.UserTag(tag, uuid))
	if !ok {
		return errors.New("not found")
	}
	info := *v.(*UserLimitInfo)
	info.DynamicSpeedLimit = limit
	info.ExpireTime = expire.Unix()
	l.storeUserLimit(tag, uuid, &info)
	return nil
}

func loadUserLimit(l *Limiter, taguuid string) (*UserLimitInfo, bool) {
	if v, ok := l.UserLimitInfo.Load(taguuid); ok {
		return v.(*UserLimitInfo), true
	}
	if i := strings.LastIndex(taguuid, "|"); i >= 0 {
		if v, ok := l.UserLimitInfo.Load(taguuid[i+1:]); ok {
			return v.(*UserLimitInfo), true
		}
	}
	return nil, false
}

func (l *Limiter) CheckLimit(taguuid string, ip string, noUDPsource bool) (DynamicBucket *rate.DynamicBucket, Reject bool) {
	// check if ipv4 mapped ipv6
	ip = strings.TrimPrefix(ip, "::ffff:")

	// check and gen speed limit Bucket
	nodeLimit := l.SpeedLimit
	userLimit := 0
	deviceLimit := 0
	var uid int
	if u, ok := loadUserLimit(l, taguuid); ok {
		deviceLimit = u.DeviceLimit
		uid = u.UID
		// An expired dynamic limit simply stops applying; the snapshot is
		// read-only here.
		if u.ExpireTime != 0 && u.ExpireTime < time.Now().Unix() {
			userLimit = u.SpeedLimit
		} else {
			userLimit = determineSpeedLimit(u.SpeedLimit, u.DynamicSpeedLimit)
		}
	} else {
		return nil, true
	}
	// WireGuard connections carry the peer's tunnel address, not the client's IP, so
	// they cannot be counted as devices.
	trackDevice := (noUDPsource || l.Nodetype == "hysteria2" || l.Nodetype == "tuic") &&
		l.Nodetype != "wireguard"
	if trackDevice {
		// Store online user for device limit
		newipMap := new(sync.Map)
		newipMap.Store(ip, uid)
		aliveIp := l.aliveCount(uid)
		oldUserOnline := l.oldUserOnline.Load()
		// If any device is online
		if v, loaded := l.UserOnlineIP.LoadOrStore(taguuid, newipMap); loaded {
			oldipMap := v.(*sync.Map)
			// If this is a new ip
			if _, loaded := oldipMap.LoadOrStore(ip, uid); !loaded {
				if v, loaded := oldUserOnline.Load(ip); loaded {
					if v.(int) == uid {
						oldUserOnline.Delete(ip)
					}
				} else if deviceLimit > 0 {
					if deviceLimit <= aliveIp && !onlineOnOtherInbound(l, taguuid, uid, ip) {
						oldipMap.Delete(ip)
						return nil, true
					}
				}
			}
		} else if v, ok := oldUserOnline.Load(ip); ok {
			if v.(int) == uid {
				oldUserOnline.Delete(ip)
			}
		} else {
			if deviceLimit > 0 {
				if deviceLimit <= aliveIp && !onlineOnOtherInbound(l, taguuid, uid, ip) {
					l.UserOnlineIP.Delete(taguuid)
					return nil, true
				}
			}
		}
	}

	limit := int64(determineSpeedLimit(nodeLimit, userLimit)) * 1000000 / 8 // If you need the Speed limit
	if limit > 0 {
		if v, ok := l.SpeedLimiter.Load(taguuid); ok {
			return v.(*rate.DynamicBucket), false
		}
		v, _ := l.SpeedLimiter.LoadOrStore(taguuid, rate.NewDynamicBucket(limit))
		return v.(*rate.DynamicBucket), false
	} else {
		return nil, false
	}
}

// onlineOnOtherInbound reports whether ip is already online for uid on another
// inbound of this node, in the current or the previous report window. Such an IP
// is already part of the panel's device count, so it is not a new device.
func onlineOnOtherInbound(self *Limiter, taguuid string, uid int, ip string) bool {
	uuid := taguuid
	if i := strings.LastIndex(taguuid, "|"); i >= 0 {
		uuid = taguuid[i+1:]
	}
	limitLock.RLock()
	defer limitLock.RUnlock()
	for tag, other := range limiter {
		if other == self {
			continue
		}
		if v, ok := other.oldUserOnline.Load().Load(ip); ok && v.(int) == uid {
			return true
		}
		if v, ok := other.UserOnlineIP.Load(format.UserTag(tag, uuid)); ok {
			if owner, ok := v.(*sync.Map).Load(ip); ok && owner.(int) == uid {
				return true
			}
		}
	}
	return false
}

func (l *Limiter) GetOnlineDevice() (*[]panel.OnlineUser, error) {
	var onlineUser []panel.OnlineUser
	oldUserOnline := new(sync.Map)
	l.oldUserOnline.Store(oldUserOnline)
	l.UserOnlineIP.Range(func(key, _ interface{}) bool {
		// Reset online device
		value, ok := l.UserOnlineIP.LoadAndDelete(key)
		if !ok {
			return true
		}
		value.(*sync.Map).Range(func(key, value interface{}) bool {
			uid := value.(int)
			ip := key.(string)
			oldUserOnline.Store(ip, uid)
			onlineUser = append(onlineUser, panel.OnlineUser{UID: uid, IP: ip})
			return true
		})
		return true
	})

	return &onlineUser, nil
}

type UserIpList struct {
	Uid    int      `json:"Uid"`
	IpList []string `json:"Ips"`
}
