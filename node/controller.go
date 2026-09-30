package node

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	panel "github.com/youtonghy/TJXT_node/api/v2board"
	"github.com/youtonghy/TJXT_node/common/task"
	"github.com/youtonghy/TJXT_node/conf"
	"github.com/youtonghy/TJXT_node/core"
	"github.com/youtonghy/TJXT_node/limiter"
)

type Controller struct {
	server                  *core.V2Core
	apiClient               *panel.Client
	tag                     string
	limiter                 *limiter.Limiter
	userList                []panel.UserInfo
	aliveMap                map[int]int
	conf                    *conf.NodeConfig
	info                    *panel.NodeInfo
	mtproto                 *mtprotoSidecar
	nodeInfoMonitorPeriodic *task.Task
	userReportPeriodic      *task.Task
	renewCertPeriodic       *task.Task
	// trafficMu serializes traffic reports so the periodic task and the final
	// flush on Close never report or acknowledge the same batch twice.
	trafficMu      sync.Mutex
	pendingTraffic *core.TrafficBatch
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
	// Update user
	c.userList, err = c.apiClient.GetUserList(context.Background())
	if err != nil {
		return fmt.Errorf("get user list error: %s", err)
	}
	if len(c.userList) == 0 {
		return errors.New("add users error: not have any user")
	}
	c.aliveMap, err = c.apiClient.GetUserAlive(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get user alive list: %s", err)
	}
	c.tag = node.Tag

	// add limiter
	l := limiter.AddLimiter(c.info.Type, c.tag, c.userList, c.aliveMap)
	c.limiter = l
	if node.Security == panel.Tls {
		err = c.requestCert()
		if err != nil {
			return fmt.Errorf("request cert error: %s", err)
		}
	}
	if err := c.applyRuntime(node, c.userList); err != nil {
		return err
	}
	c.info = node
	c.startTasks(node)
	return nil
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
	c.finalFlush()
	if c.mtproto != nil {
		c.mtproto.Stop()
		c.mtproto = nil
	}
	if c.info != nil && c.info.Type == "mtproto" {
		return nil
	}
	err := c.server.DelNode(c.tag)
	if err != nil {
		return fmt.Errorf("del node error: %s", err)
	}
	return nil
}

// finalFlushTimeout bounds the traffic report attempted while closing.
const finalFlushTimeout = 15 * time.Second

// finalFlush reports the pending batch and then all remaining traffic before the
// inbound and its counters go away. Failures are logged; the counters are lost
// only if the panel is unreachable during shutdown or reload.
func (c *Controller) finalFlush() {
	if c.server == nil || c.apiClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), finalFlushTimeout)
	defer cancel()
	// The first call drains a pending batch, the second one the rest.
	for range 2 {
		traffic, err := c.flushTraffic(ctx, 0)
		if err != nil {
			log.WithFields(log.Fields{"tag": c.tag, "err": err}).
				Error("Final traffic report failed, unreported traffic is dropped")
			return
		}
		if len(traffic) == 0 {
			return
		}
	}
}

func (c *Controller) applyRuntime(node *panel.NodeInfo, users []panel.UserInfo) error {
	if node.Type == "mtproto" {
		if len(users) == 0 {
			if c.mtproto != nil {
				c.mtproto.Stop()
				c.mtproto = nil
			}
			log.WithField("tag", c.tag).Info("Stopped mtproto sidecar because user list is empty")
			return nil
		}
		if c.mtproto != nil {
			c.mtproto.Stop()
			c.mtproto = nil
		}
		sidecar, err := startMTProto(node, users)
		if err != nil {
			return fmt.Errorf("start mtproto sidecar: %s", err)
		}
		c.mtproto = sidecar
		log.WithField("tag", c.tag).Infof("Started mtproto sidecar for %d users", len(users))
		return nil
	}
	if len(users) == 0 {
		log.WithField("tag", c.tag).Info("Left inbound down because user list is empty")
		return nil
	}
	err := c.server.AddNode(c.tag, node, users)
	if err != nil {
		return fmt.Errorf("add new node error: %s", err)
	}
	added, err := c.server.AddUsers(&core.AddUsersParams{
		Tag:      c.tag,
		Users:    users,
		NodeInfo: node,
	})
	if err != nil {
		return fmt.Errorf("add users error: %s", err)
	}
	log.WithField("tag", c.tag).Infof("Added %d new users", added)
	return nil
}
