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
		infos[i] = info
	}
	return FromInfos(nodes, infos)
}

// FromInfos builds nodes from already fetched node info without asking the
// panel, e.g. to restart the last working configuration after a failed reload.
func FromInfos(nodes []conf.NodeConfig, infos []*panel.NodeInfo) (*Node, error) {
	if len(nodes) != len(infos) {
		return nil, fmt.Errorf("have %d node configs but %d node infos", len(nodes), len(infos))
	}
	n := &Node{
		controllers: make([]*Controller, len(nodes)),
		NodeInfos:   infos,
	}
	for i := range nodes {
		p, err := panel.New(&nodes[i])
		if err != nil {
			return nil, err
		}
		n.controllers[i] = NewController(p, &nodes[i], infos[i])
	}
	return n, nil
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
