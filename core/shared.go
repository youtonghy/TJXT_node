package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/common/counter"
	"github.com/youtonghy/TJXT_node/common/format"
)

// sharedInbound is an inbound whose users belong to several nodes: the node
// that owns it and the nodes sharing it (lines). Each member's users carry the
// member's tag in their email, so traffic, limits and routing stay per node. It
// runs while any member has users.
type sharedInbound struct {
	// info is the configuration the inbound was built with.
	info    *panel.NodeInfo
	members map[string][]panel.UserInfo
}

func (s *sharedInbound) userCount() int {
	n := 0
	for _, users := range s.members {
		n += len(users)
	}
	return n
}

// SharesInbound reports whether a node type can share one inbound between
// several nodes: its users are added and removed on the running inbound.
func SharesInbound(nodeType string) bool {
	switch nodeType {
	case "vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic", "anytls":
		return true
	default:
		return false
	}
}

// SetInboundMember makes users the users member has in the shared inbound
// inbound: it starts the inbound (built from info) when the first users of any
// member arrive, adds and removes the member's users on it, and stops it when
// no member has users left. A nil users leaves the inbound.
func (v *V2Core) SetInboundMember(inbound, member string, info *panel.NodeInfo, users []panel.UserInfo) error {
	if !SharesInbound(info.Type) {
		return fmt.Errorf("%s nodes cannot share an inbound", info.Type)
	}
	v.sharedMu.Lock()
	defer v.sharedMu.Unlock()
	if v.shared == nil {
		v.shared = make(map[string]*sharedInbound)
	}
	s := v.shared[inbound]
	if s == nil {
		if len(users) == 0 {
			return nil
		}
		s = &sharedInbound{members: make(map[string][]panel.UserInfo)}
	}
	old := s.members[member]
	removed, added := diffUsers(old, users)
	running := s.info != nil
	if running && len(removed) > 0 {
		if err := v.removeMemberUsers(inbound, member, removed); err != nil {
			return err
		}
	}
	if len(users) == 0 {
		delete(s.members, member)
	} else {
		s.members[member] = users
	}
	if s.userCount() == 0 {
		delete(v.shared, inbound)
		if running {
			return v.DelNode(inbound)
		}
		return nil
	}
	v.shared[inbound] = s
	if !running {
		// The first users of any member start the inbound with everyone's users.
		return v.startShared(inbound, s, info)
	}
	return v.addMemberUsers(inbound, member, info, added)
}

// RebuildInbound applies new settings to a running shared inbound: it is built
// again from info and every member's users are added back.
func (v *V2Core) RebuildInbound(inbound string, info *panel.NodeInfo) error {
	v.sharedMu.Lock()
	defer v.sharedMu.Unlock()
	s := v.shared[inbound]
	if s == nil || s.info == nil {
		return nil
	}
	if err := v.DelNode(inbound); err != nil {
		return err
	}
	s.info = nil
	return v.startShared(inbound, s, info)
}

// startShared builds the inbound and adds every member's users; on failure the
// inbound stays down and is built again by the next member update.
func (v *V2Core) startShared(inbound string, s *sharedInbound, info *panel.NodeInfo) error {
	if err := v.AddNode(inbound, info, nil); err != nil {
		return err
	}
	s.info = info
	for member, users := range s.members {
		if err := v.addMemberUsers(inbound, member, info, users); err != nil {
			s.info = nil
			return errors.Join(err, v.DelNode(inbound))
		}
	}
	return nil
}

func (v *V2Core) addMemberUsers(inbound, member string, info *panel.NodeInfo, users []panel.UserInfo) error {
	if len(users) == 0 {
		return nil
	}
	_, err := v.AddUsers(&AddUsersParams{Tag: member, Inbound: inbound, Users: users, NodeInfo: info})
	return err
}

// removeMemberUsers removes users of member from the inbound and drops their
// counters (their traffic was reported before).
func (v *V2Core) removeMemberUsers(inbound, member string, users []panel.UserInfo) error {
	man, err := v.GetUserManager(inbound)
	if err != nil {
		return fmt.Errorf("get user manager error: %s", err)
	}
	v.users.mapLock.Lock()
	defer v.users.mapLock.Unlock()
	for i := range users {
		email := format.UserTag(member, users[i].Uuid)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := man.RemoveUser(ctx, email)
		cancel()
		if err != nil {
			return err
		}
		delete(v.users.uidMap, email)
		delete(v.users.uidMap, users[i].Uuid)
		if c, ok := v.dispatcher.Counter.Load(member); ok {
			c.(*counter.TrafficCounter).Delete(email)
		}
	}
	return nil
}

// diffUsers returns the users of old missing from new and those of new missing
// from old, by credential.
func diffUsers(old, new []panel.UserInfo) (removed, added []panel.UserInfo) {
	inOld := make(map[string]bool, len(old))
	for _, u := range old {
		inOld[u.Uuid] = true
	}
	inNew := make(map[string]bool, len(new))
	for _, u := range new {
		inNew[u.Uuid] = true
		if !inOld[u.Uuid] {
			added = append(added, u)
		}
	}
	for _, u := range old {
		if !inNew[u.Uuid] {
			removed = append(removed, u)
		}
	}
	return removed, added
}
