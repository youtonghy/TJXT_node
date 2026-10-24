package node

import (
	"context"
	"errors"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/common/task"
)

func (c *Controller) startTasks(node *panel.NodeInfo) {
	// fetch node info task
	c.nodeInfoMonitorPeriodic = &task.Task{
		Name:     "nodeInfoMonitor",
		Interval: node.PullInterval,
		Execute:  c.nodeInfoMonitor,
	}
	// fetch user list task
	c.userReportPeriodic = &task.Task{
		Name:     "reportUserTrafficTask",
		Interval: node.PushInterval,
		Execute:  c.reportUserTrafficTask,
	}
	log.WithField("tag", c.tag).Info("Start monitor node status")
	// delay to start nodeInfoMonitor
	c.startTask(c.nodeInfoMonitorPeriodic, false)
	log.WithField("tag", c.tag).Info("Start report node status")
	// Report once right away so the panel sees the node's state without
	// waiting a full push interval. It runs here rather than as the task's
	// first tick so it cannot interleave with whatever the caller does next;
	// with nothing to report it makes no request.
	ctx, cancel := context.WithTimeout(context.Background(), finalFlushTimeout)
	if err := c.reportUserTrafficTask(ctx); err != nil {
		log.WithFields(log.Fields{"tag": c.tag, "err": err}).Warn("Initial node status report failed, retrying on the next interval")
	}
	cancel()
	c.startTask(c.userReportPeriodic, false)
	if node.Security == panel.Tls {
		switch c.nodeInfo().Common.CertInfo.CertMode {
		case "none", "", "file", "self":
		default:
			c.renewCertPeriodic = &task.Task{
				Name:     "renewCertTask",
				Interval: time.Hour * 24,
				Execute:  c.renewCertTask,
			}
			log.WithField("tag", c.tag).Info("Start renew cert")
			// delay to start renewCert
			c.startTask(c.renewCertPeriodic, true)
		}
	}
}

func (c *Controller) startTask(t *task.Task, first bool) {
	if err := t.Start(first); err != nil {
		log.WithFields(log.Fields{"tag": c.tag, "err": err}).Error("Start task failed")
	}
}

func (c *Controller) nodeInfoMonitor(ctx context.Context) (err error) {
	// get node info
	newN, err := c.apiClient.GetNodeInfo(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get node info failed")
		return nil
	}
	if newN != nil && reloadsInPlace(c.nodeInfo(), newN) {
		c.reloadInbound(newN)
	} else if newN != nil {
		log.WithField("tag", c.tag).Info("Got new node info, reload")
		if c.server.ReloadCh != nil {
			select {
			case c.server.ReloadCh <- struct{}{}:
			default:
			}
		} else {
			log.WithField("tag", c.tag).Error("No reload channel, the new node info is ignored")
		}
	} else {
		log.WithField("tag", c.tag).Debug("Node info no change")
	}

	// get user info
	newU, err := c.apiClient.GetUserList(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get user list failed")
		return nil
	}
	// get user alive
	newA, err := c.apiClient.GetUserAlive(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		// Keep the previous alive list; it must not block user updates.
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get alive list failed")
	} else if newA != nil {
		c.aliveMap = newA
		c.limiter.SetAliveList(newA)
	}
	c.usersMu.Lock()
	defer c.usersMu.Unlock()
	// nil means unchanged (304); an empty slice means the panel removed everyone.
	// A changed list supersedes revocations from push answers.
	if newU != nil {
		c.desiredUsers = newU
		c.revoked = nil
	}
	if !c.ensureCert() {
		return nil
	}
	if newU == nil && !c.resync {
		log.WithField("tag", c.tag).Debug("User list no change")
		return nil
	}
	if err := c.syncUsers(); err != nil {
		log.WithFields(log.Fields{"tag": c.tag, "err": err}).Error("Apply user list failed, retrying on next poll")
	}
	return nil
}
