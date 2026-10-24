package node

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/common/task"
	"github.com/youtonghy/TJXT_node/conf"
	"github.com/youtonghy/TJXT_node/core"
	"github.com/youtonghy/TJXT_node/limiter"
)

type Controller struct {
	server    *core.V2Core
	apiClient *panel.Client
	tag       string
	limiter   *limiter.Limiter
	// userList is the applied user list; desiredUsers the panel's latest list.
	userList     []panel.UserInfo
	desiredUsers []panel.UserInfo
	resync       bool
	// certPending is set while a TLS node has no usable certificate; its
	// inbound stays down and each poll requests the certificate again.
	certPending             bool
	aliveMap                map[int]int
	conf                    *conf.NodeConfig
	info                    *panel.NodeInfo
	nodeInfoMonitorPeriodic *task.Task
	userReportPeriodic      *task.Task
	renewCertPeriodic       *task.Task
	// trafficMu serializes traffic reports so the periodic task and the final
	// flush on Close never report or acknowledge the same batch twice.
	trafficMu      sync.Mutex
	pendingTraffic *core.TrafficBatch
	// pendingRejects counts permanent rejections of pendingTraffic.
	pendingRejects int
	// revokedByPush collects users the panel revoked in push answers, until the
	// report task removes them (guarded by trafficMu).
	revokedByPush []int
	// usersMu serializes changes of the user lists (polls and revocations).
	usersMu sync.Mutex
	// revoked users stay removed until the panel sends a changed user list.
	revoked map[int]bool
	// infoMu guards info for the tasks that read it while a poll replaces it.
	infoMu sync.RWMutex
}

// nodeInfo returns the node config currently applied.
func (c *Controller) nodeInfo() *panel.NodeInfo {
	c.infoMu.RLock()
	defer c.infoMu.RUnlock()
	return c.info
}

// NewController return a Node controller with default parameters.
func NewController(api *panel.Client, conf *conf.NodeConfig, info *panel.NodeInfo) *Controller {
	controller := &Controller{
		apiClient: api,
		info:      info,
		conf:      conf,
	}
	return controller
}

// Start implement the Start() function of the service interface
func (c *Controller) Start(x *core.V2Core) error {
	// Init Core
	c.server = x
	var err error
	// First fetch Node Info
	node := c.info
	if node == nil {
		c.info, err = c.apiClient.GetNodeInfo(context.Background())
		if err != nil {
			return fmt.Errorf("get node info error: %s", err)
		}
		node = c.info
	}
	// Update user. A panel that is unreachable now must not keep this node
	// (or the others) from starting: the inbound stays down and the next poll
	// fetches the list again.
	users, err := c.apiClient.GetUserList(context.Background())
	if err != nil {
		log.WithFields(log.Fields{"tag": node.Tag, "err": err}).
			Error("Get user list failed, the inbound stays down until the next poll gets it")
		users = nil
	}
	if users == nil {
		users = []panel.UserInfo{}
	}
	c.aliveMap, err = c.apiClient.GetUserAlive(context.Background())
	if err != nil {
		// The alive list only refines device limits; start without it.
		log.WithFields(log.Fields{"tag": node.Tag, "err": err}).Warn("Get alive list failed, device limits use local counts only")
		c.aliveMap = map[int]int{}
	}
	c.tag = node.Tag

	// add limiter; syncUsers fills it with the users it applies.
	c.limiter = limiter.AddLimiter(c.info.Type, c.tag, nil, c.aliveMap)
	c.info = node
	// An empty list is valid: the inbound stays down and polling brings it up
	// as soon as the panel sends users.
	c.desiredUsers = users
	c.certPending = node.Security == panel.Tls
	if !c.ensureCert() {
		// Like a broken inbound below, a missing certificate only keeps this
		// node down; the other nodes start and this one retries on each poll.
		c.resync = true
	} else if err := c.syncUsers(); err != nil {
		// A node whose inbound cannot be built (e.g. a bad panel config) must not
		// take the other nodes of this process down. syncUsers left the runtime
		// reset, so the next poll retries from scratch, and a fixed panel config
		// reloads the node.
		log.WithFields(log.Fields{"tag": c.tag, "err": err}).
			Error("Apply node failed, the other nodes keep running and this one retries on the next poll")
	}
	c.startTasks(node)
	return nil
}

// ensureCert requests the certificate of a TLS node that does not have one
// yet and reports whether the inbound can be built.
func (c *Controller) ensureCert() bool {
	if !c.certPending {
		return true
	}
	if err := c.requestCert(); err != nil {
		log.WithFields(log.Fields{"tag": c.tag, "err": err}).
			Error("Request cert failed, this node stays down and retries on the next poll; the other nodes keep running")
		return false
	}
	c.certPending = false
	c.resync = true
	c.reportCertFingerprint()
	return true
}

// Close implement the Close() function of the service interface
func (c *Controller) Close() error {
	limiter.DeleteLimiter(c.tag)
	if c.nodeInfoMonitorPeriodic != nil {
		c.nodeInfoMonitorPeriodic.Close()
	}
	if c.userReportPeriodic != nil {
		c.userReportPeriodic.Close()
	}
	if c.renewCertPeriodic != nil {
		c.renewCertPeriodic.Close()
	}
	c.flushAll("closing")
	if err := c.stopRuntime(); err != nil {
		return fmt.Errorf("del node error: %s", err)
	}
	return nil
}

// finalFlushTimeout bounds the traffic report attempted while closing.
const finalFlushTimeout = 15 * time.Second

// flushAll reports the pending batch and then all remaining traffic before the
// inbound or some users' counters go away (closing, removing users). Failures
// are logged; the counters are lost only if the panel is unreachable then.
func (c *Controller) flushAll(reason string) {
	if c.server == nil || c.apiClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), finalFlushTimeout)
	defer cancel()
	// The first call drains a pending batch, the second one the rest.
	for range 2 {
		traffic, err := c.flushTraffic(ctx, 0)
		if err != nil {
			c.spoolRemaining(reason, err)
			return
		}
		if len(traffic) == 0 {
			return
		}
	}
}

// spoolRemaining keeps the traffic the panel did not accept (the pending batch and
// whatever the counters still hold) on disk, since the counters are about to go
// away; the next push reports it under its original batch IDs.
func (c *Controller) spoolRemaining(reason string, reportErr error) {
	c.trafficMu.Lock()
	defer c.trafficMu.Unlock()
	var batches []spooledBatch
	if c.pendingTraffic != nil {
		batches = append(batches, spooledBatch{ID: c.pendingTraffic.ID, Traffic: c.pendingTraffic.Traffic()})
	}
	// The pending batch's bytes are still in the counters: take them out before
	// snapshotting the rest, so nothing is spooled twice.
	c.server.AckTraffic(c.pendingTraffic)
	c.pendingTraffic = nil
	c.pendingRejects = 0
	rest := c.server.SnapshotTraffic(c.tag, 0)
	if rest != nil {
		batches = append(batches, spooledBatch{ID: rest.ID, Traffic: rest.Traffic()})
		c.server.AckTraffic(rest)
	}
	fields := log.Fields{"tag": c.tag, "reason": reason, "report_err": reportErr, "batches": len(batches)}
	if len(batches) == 0 {
		return
	}
	if err := spoolTraffic(c.tag, batches...); err != nil {
		fields["err"] = err
		log.WithFields(fields).Error("Traffic report failed and keeping it on disk failed too, unreported traffic is dropped")
		return
	}
	log.WithFields(fields).Warn("Traffic report failed, kept on disk and reported on the next push")
}

// reloadsInPlace reports whether moving from old to new changes only this node's
// inbound (protocol, transport, TLS/REALITY, port, ...), which the controller can
// rebuild alone. Routes, private egress and the rest of the base config shape the
// shared Xray router and outbounds, and the tag and intervals the tasks, so those
// changes still restart the core.
func reloadsInPlace(old, new *panel.NodeInfo) bool {
	if old == nil || new == nil || old.Common == nil || new.Common == nil {
		return false
	}
	return old.Id == new.Id && old.Type == new.Type && old.Tag == new.Tag &&
		old.PushInterval == new.PushInterval && old.PullInterval == new.PullInterval &&
		reflect.DeepEqual(old.Common.Routes, new.Common.Routes) &&
		reflect.DeepEqual(old.Common.BaseConfig, new.Common.BaseConfig)
}

// reloadInbound applies a new config of this node by rebuilding only its inbound;
// the other nodes keep their connections. The next sync of this poll brings the
// inbound back with the current users. Traffic counters are per tag and survive.
func (c *Controller) reloadInbound(info *panel.NodeInfo) {
	c.usersMu.Lock()
	defer c.usersMu.Unlock()
	certChanged := !reflect.DeepEqual(c.info.Common.CertInfo, info.Common.CertInfo)
	c.infoMu.Lock()
	c.info = info
	c.infoMu.Unlock()
	if info.Security == panel.Tls && certChanged {
		c.certPending = true
	}
	c.resetRuntime()
	log.WithField("tag", c.tag).Info("Node config changed, rebuilding this inbound only")
}

// takeRevoked returns and clears the users revoked by push answers.
func (c *Controller) takeRevoked() []int {
	c.trafficMu.Lock()
	defer c.trafficMu.Unlock()
	ids := c.revokedByPush
	c.revokedByPush = nil
	return ids
}

// revokeUsers disconnects users the panel says lost access, without waiting for
// the next user list poll. They stay removed until the panel sends a changed
// list, which is then applied as is (a stale list can only delay a revocation).
func (c *Controller) revokeUsers(ids []int) {
	c.usersMu.Lock()
	defer c.usersMu.Unlock()
	if c.revoked == nil {
		c.revoked = make(map[int]bool)
	}
	for _, id := range ids {
		c.revoked[id] = true
	}
	kept := make([]panel.UserInfo, 0, len(c.desiredUsers))
	for _, user := range c.desiredUsers {
		if !c.revoked[user.Id] {
			kept = append(kept, user)
		}
	}
	if len(kept) == len(c.desiredUsers) {
		return
	}
	c.desiredUsers = kept
	if err := c.syncUsers(); err != nil {
		log.WithFields(log.Fields{"tag": c.tag, "err": err}).Error("Removing revoked users failed, retrying on next poll")
		return
	}
	log.WithFields(log.Fields{"tag": c.tag, "users": ids}).Info("Disconnected users the panel revoked")
}

// syncUsers moves the runtime from the applied user list (c.userList) to the
// panel's list (c.desiredUsers). The inbound runs exactly
// when the applied list is non-empty: the first users start it, the last removed
// user stops it. If applying fails, the runtime is torn down and rebuilt from
// scratch on the next poll, even if the panel then answers "unchanged".
func (c *Controller) syncUsers() error {
	desired := c.desiredUsers
	deleted, added, modified := compareUserList(c.userList, desired)
	if !c.resync && len(deleted) == 0 && len(added) == 0 && len(modified) == 0 {
		return nil
	}
	if len(deleted) > 0 && len(c.userList) > 0 {
		// Removing users deletes their counters; report them first.
		c.flushAll("removing users")
	}
	if err := c.applyUserDiff(desired, deleted, added); err != nil {
		c.resetRuntime()
		return err
	}
	c.limiter.UpdateUser(c.tag, added, deleted, modified)
	c.userList = desired
	c.resync = false
	log.WithField("tag", c.tag).Infof("%d user deleted, %d user added, %d user modified", len(deleted), len(added), len(modified))
	return nil
}

func (c *Controller) applyUserDiff(desired, deleted, added []panel.UserInfo) error {
	running := len(c.userList) > 0
	if len(deleted) > 0 && running {
		if err := c.server.DelUsers(deleted, c.tag, c.info); err != nil {
			return fmt.Errorf("delete users error: %s", err)
		}
	}
	switch {
	case len(desired) == 0:
		if err := c.server.DelNode(c.tag); err != nil {
			return fmt.Errorf("stop inbound error: %s", err)
		}
		if running {
			log.WithField("tag", c.tag).Info("Stopped inbound because user list is empty")
		} else {
			log.WithField("tag", c.tag).Info("Left inbound down because user list is empty")
		}
		return nil
	case !running || c.resync || core.RebuildsOnUserChange(c.info.Type) && (len(deleted) > 0 || len(added) > 0):
		// (Re)build the inbound with the full list; removal is idempotent.
		if err := c.server.DelNode(c.tag); err != nil {
			return fmt.Errorf("remove inbound error: %s", err)
		}
		if err := c.server.AddNode(c.tag, c.info, desired); err != nil {
			return fmt.Errorf("add new node error: %s", err)
		}
		added = desired
	}
	if len(added) == 0 {
		return nil
	}
	n, err := c.server.AddUsers(&core.AddUsersParams{
		Tag:      c.tag,
		Users:    added,
		NodeInfo: c.info,
	})
	if err != nil {
		return fmt.Errorf("add users error: %s", err)
	}
	log.WithField("tag", c.tag).Infof("Added %d new users", n)
	return nil
}

// resetRuntime tears the runtime down after a failed sync so the next sync
// rebuilds it from an empty state.
func (c *Controller) resetRuntime() {
	if err := c.stopRuntime(); err != nil {
		log.WithFields(log.Fields{"tag": c.tag, "err": err}).Error("Stop inbound after failed user sync failed")
	}
	// The limiter still mirrors the last applied list; empty it in place so
	// concurrent readers keep a valid limiter.
	c.limiter.UpdateUser(c.tag, nil, c.userList, nil)
	c.userList = nil
	c.resync = true
}

// stopRuntime stops the inbound; stopping one that is not running is not an error.
func (c *Controller) stopRuntime() error {
	if c.server == nil || c.info == nil {
		return nil
	}
	return c.server.DelNode(c.tag)
}
