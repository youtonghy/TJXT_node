package node

import (
	"context"
	"errors"
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/youtonghy/TJXT_node/api/panel"
)

func (c *Controller) reportUserTrafficTask(ctx context.Context) (err error) {
	var devicemin = 0
	if info := c.nodeInfo(); info.Common.BaseConfig != nil {
		devicemin = info.Common.BaseConfig.DeviceOnlineMinTraffic
	}
	if err := c.reportSpool(ctx); err != nil {
		log.WithFields(log.Fields{"tag": c.tag, "err": err}).Warn("Reporting spooled traffic failed, it is kept and retried")
	}
	userTraffic, err := c.flushTraffic(ctx, c.reportMinTraffic())
	if revoked := c.takeRevoked(); len(revoked) > 0 {
		c.revokeUsers(revoked)
	}
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Warn("Report user traffic failed, the batch is kept and retried")
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}

	if onlineDevice, err := c.limiter.GetOnlineDevice(); err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Info("Get online device failed")
	} else if len(*onlineDevice) > 0 {
		var result []panel.OnlineUser
		var nocountUID = make(map[int]struct{})
		for _, traffic := range userTraffic {
			total := traffic.Upload + traffic.Download
			if total < int64(devicemin*1000) {
				nocountUID[traffic.UID] = struct{}{}
			}
		}
		for _, online := range *onlineDevice {
			if _, ok := nocountUID[online.UID]; !ok {
				result = append(result, online)
			}
		}
		data := make(map[int][]string)
		for _, onlineuser := range result {
			// json structure: { UID1:["ip1","ip2"],UID2:["ip3","ip4"] }
			data[onlineuser.UID] = append(data[onlineuser.UID], onlineuser.IP)
		}
		if len(data) != 0 {
			err := c.apiClient.ReportNodeOnlineUsers(ctx, &data)
			if err != nil {
				log.WithFields(log.Fields{
					"tag": c.tag,
					"err": err,
				}).Info("Report online users failed")
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
			}
		}
		log.WithField("tag", c.tag).Infof("Total %d online users, %d Reported", len(*onlineDevice), len(result))
	}

	return nil
}

func (c *Controller) reportMinTraffic() int {
	if info := c.nodeInfo(); info != nil && info.Common != nil && info.Common.BaseConfig != nil {
		return info.Common.BaseConfig.NodeReportMinTraffic
	}
	return 0
}

// maxBatchRejects is how many permanent (4xx) rejections a traffic batch gets
// before it is dropped.
const maxBatchRejects = 3

// flushTraffic reports the pending traffic batch, or a new snapshot when nothing
// is pending. A batch stays pending (same ID, same content) until the panel
// acknowledges it; only then are its bytes subtracted from the live counters.
// It returns the traffic of the batch it tried to report.
func (c *Controller) flushTraffic(ctx context.Context, minTraffic int) ([]panel.UserTraffic, error) {
	c.trafficMu.Lock()
	defer c.trafficMu.Unlock()
	if c.pendingTraffic == nil {
		c.pendingTraffic = c.server.SnapshotTraffic(c.tag, minTraffic)
		if c.pendingTraffic == nil {
			return nil, nil
		}
	}
	batch := c.pendingTraffic
	userTraffic := batch.Traffic()
	result, err := c.apiClient.ReportUserTraffic(ctx, batch.ID, userTraffic)
	if err != nil {
		var statusErr *panel.StatusError
		if errors.As(err, &statusErr) && statusErr.Permanent() {
			c.pendingRejects++
			if c.pendingRejects >= maxBatchRejects {
				// Resending cannot succeed and would block every later report.
				log.WithFields(log.Fields{
					"tag":     c.tag,
					"batch":   batch.ID,
					"traffic": userTraffic,
					"err":     err,
				}).Error("Panel keeps rejecting the traffic batch, dropping it")
				c.server.AckTraffic(batch)
				c.pendingTraffic = nil
				c.pendingRejects = 0
			}
		}
		return userTraffic, fmt.Errorf("batch %s: %w", batch.ID, err)
	}
	c.server.AckTraffic(batch)
	c.pendingTraffic = nil
	c.pendingRejects = 0
	if result != nil && len(result.RevokedUserIDs) > 0 {
		c.revokedByPush = append(c.revokedByPush, result.RevokedUserIDs...)
	}
	if result != nil && len(result.RejectedUserIDs) > 0 {
		log.WithFields(log.Fields{
			"tag":   c.tag,
			"batch": batch.ID,
			"users": result.RejectedUserIDs,
		}).Warn("Panel did not bill traffic of some users")
	}
	log.WithField("tag", c.tag).Infof("Report %d users traffic", len(userTraffic))
	return userTraffic, nil
}

func compareUserList(old, new []panel.UserInfo) (deleted, added, modified []panel.UserInfo) {
	oldMap := make(map[string]panel.UserInfo, len(old))
	for _, u := range old {
		oldMap[u.Uuid] = u
	}

	for _, u := range new {
		if o, ok := oldMap[u.Uuid]; !ok {
			added = append(added, u)
		} else {
			if o.SpeedLimit != u.SpeedLimit || o.DeviceLimit != u.DeviceLimit {
				modified = append(modified, u)
			}
			delete(oldMap, u.Uuid)
		}
	}

	for _, o := range oldMap {
		deleted = append(deleted, o)
	}

	return deleted, added, modified
}
