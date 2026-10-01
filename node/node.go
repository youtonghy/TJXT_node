package node

import (
	"context"
	"errors"
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/conf"
	"github.com/youtonghy/TJXT_node/core"
)

type Node struct {
	controllers []*Controller
	NodeInfos   []*panel.NodeInfo
}

func New(nodes []conf.NodeConfig) (*Node, error) {
	clients := make([]*panel.Client, len(nodes))
	infos := make([]*panel.NodeInfo, len(nodes))
	for i := range nodes {
		p, err := panel.New(&nodes[i])
		if err != nil {
			return nil, err
		}
		info, err := p.GetNodeInfo(context.Background())
		if err != nil {
			return nil, err
		}
		clients[i], infos[i] = p, info
	}
	// The controllers keep these clients: they hold the ETag and hash of the
	// config just fetched, so the first poll does not see it as a change.
	return build(nodes, infos, clients), nil
}

// FromInfos builds nodes from already fetched node info without asking the
// panel, e.g. to restart the last working configuration after a failed reload.
// Its fresh clients report the panel's current config as new on the first poll.
func FromInfos(nodes []conf.NodeConfig, infos []*panel.NodeInfo) (*Node, error) {
	if len(nodes) != len(infos) {
		return nil, fmt.Errorf("have %d node configs but %d node infos", len(nodes), len(infos))
	}
	clients := make([]*panel.Client, len(nodes))
	for i := range nodes {
		p, err := panel.New(&nodes[i])
		if err != nil {
			return nil, err
		}
		clients[i] = p
	}
	return build(nodes, infos, clients), nil
}

func build(nodes []conf.NodeConfig, infos []*panel.NodeInfo, clients []*panel.Client) *Node {
	n := &Node{
		controllers: make([]*Controller, len(nodes)),
		NodeInfos:   infos,
	}
	for i := range nodes {
		n.controllers[i] = NewController(clients[i], &nodes[i], infos[i])
	}
	return n
}

func (n *Node) Start(nodes []conf.NodeConfig, core *core.V2Core) error {
	for i, node := range nodes {
		err := n.controllers[i].Start(core)
		if err != nil {
			return fmt.Errorf("start node controller [%s-%d] error: %s",
				node.APIHost,
				node.NodeID,
				err)
		}
	}
	return nil
}

// Close closes every controller, even if some fail, and returns their errors.
func (n *Node) Close() error {
	var errs []error
	for _, c := range n.controllers {
		if err := c.Close(); err != nil {
			log.Errorf("close controller failed: %v", err)
			errs = append(errs, err)
		}
	}
	n.controllers = nil
	return errors.Join(errs...)
}
