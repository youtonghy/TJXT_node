package node

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/conf"
	"github.com/youtonghy/TJXT_node/core"
)

// Backoff between attempts to fetch the config of a node that could not get
// it; variables so tests can shorten them.
var (
	retryBackoffMin = time.Second
	retryBackoffMax = time.Minute
)

// errNoNodeConfig is a first config fetch that returned nothing (304 or null).
var errNoNodeConfig = errors.New("panel returned no node config")

// Node is one generation of nodes. Nodes whose config arrived start with it
// (Configs and NodeInfos, in config order); the others are pending and retry
// in the background. When a pending node gets its config, Node signals the
// core's reload channel, and the next generation built from this one starts it
// with the config already fetched.
type Node struct {
	controllers []*Controller
	Configs     []conf.NodeConfig
	NodeInfos   []*panel.NodeInfo
	pending     []*pendingNode

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// pendingNode is a configured node without a config from the panel yet.
type pendingNode struct {
	conf conf.NodeConfig

	mu     sync.Mutex
	client *panel.Client
	info   *panel.NodeInfo
}

func (p *pendingNode) recovered() (*panel.Client, *panel.NodeInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.client, p.info
}

// New fetches every node's config concurrently. prev is the running
// generation (or nil): a node a retrier of prev already fetched is taken over
// as is, and a node whose fetch fails now keeps prev's config if it was
// running with the same settings. Any other node that fails is pending: it
// does not block the others and retries with backoff once started.
func New(nodes []conf.NodeConfig, prev *Node) (*Node, error) {
	type result struct {
		client *panel.Client
		info   *panel.NodeInfo
		err    error
	}
	results := make([]result, len(nodes))
	var wg sync.WaitGroup
	for i := range nodes {
		if client, info := prev.recoveredFor(nodes[i]); info != nil {
			results[i] = result{client: client, info: info}
			continue
		}
		wg.Go(func() {
			client, info, err := fetchNode(context.Background(), &nodes[i])
			results[i] = result{client: client, info: info, err: err}
		})
	}
	wg.Wait()

	n := &Node{}
	var clients []*panel.Client
	for i, r := range results {
		if r.err != nil {
			running := prev.runningInfo(nodes[i])
			if running == nil {
				logNodeFailure(nodes[i], r.err, retryBackoffMin)
				n.pending = append(n.pending, &pendingNode{conf: nodes[i]})
				continue
			}
			// Keep serving with the last known config; the fresh client sees
			// the panel's config as new on its first successful poll and
			// reloads then.
			client, err := panel.New(&nodes[i])
			if err != nil {
				return nil, err
			}
			r.client, r.info = client, running
			log.WithFields(nodeFields(nodes[i], r.err)).Warn("Fetching node config failed, keeping the running config of this node")
		}
		n.Configs = append(n.Configs, nodes[i])
		n.NodeInfos = append(n.NodeInfos, r.info)
		clients = append(clients, r.client)
	}
	// The controllers keep these clients: they hold the ETag and hash of the
	// config just fetched, so the first poll does not see it as a change.
	n.buildControllers(clients)
	return n, nil
}

// Rebuild returns a new generation with the same nodes and configs and fresh
// clients, without asking the panel, e.g. to restart the last working
// configuration after a failed reload. Its fresh clients report the panel's
// current config as new on the first poll.
func (n *Node) Rebuild() (*Node, error) {
	again := &Node{
		Configs: append([]conf.NodeConfig(nil), n.Configs...),
	}
	for i := range n.Configs {
		again.NodeInfos = append(again.NodeInfos, n.currentInfo(i))
	}
	clients := make([]*panel.Client, len(again.Configs))
	for i := range again.Configs {
		p, err := panel.New(&again.Configs[i])
		if err != nil {
			return nil, err
		}
		clients[i] = p
	}
	for _, p := range n.pending {
		client, info := p.recovered()
		again.pending = append(again.pending, &pendingNode{conf: p.conf, client: client, info: info})
	}
	again.buildControllers(clients)
	return again, nil
}

func (n *Node) buildControllers(clients []*panel.Client) {
	n.controllers = make([]*Controller, len(n.Configs))
	for i := range n.Configs {
		n.controllers[i] = NewController(clients[i], &n.Configs[i], n.NodeInfos[i])
	}
}

// Pending returns how many configured nodes wait for their panel config.
func (n *Node) Pending() int {
	return len(n.pending)
}

// Start starts the controllers and the retries of the pending nodes, which
// signal x.ReloadCh once their config arrives.
func (n *Node) Start(x *core.V2Core) error {
	for i, c := range n.controllers {
		if err := c.Start(x); err != nil {
			return fmt.Errorf("start node controller [%s-%d] error: %s",
				n.Configs[i].APIHost,
				n.NodeInfos[i].Id,
				err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	for _, p := range n.pending {
		if _, info := p.recovered(); info != nil {
			// Fetched already; the reload it asked for starts it.
			continue
		}
		n.wg.Go(func() { retryPending(ctx, p, x.ReloadCh) })
	}
	return nil
}

// retryPending fetches a pending node's config with exponential backoff until
// it arrives or ctx ends, then keeps it for the next generation and asks for a
// reload.
func retryPending(ctx context.Context, p *pendingNode, reload chan struct{}) {
	backoff := retryBackoffMin
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		client, info, err := fetchNode(ctx, &p.conf)
		if err == nil {
			p.mu.Lock()
			p.client, p.info = client, info
			p.mu.Unlock()
			log.WithFields(nodeFields(p.conf, nil)).Info("Node config arrived, reloading to start the node")
			select {
			case reload <- struct{}{}:
			default: // a reload is queued already and will take this node
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		backoff = min(backoff*2, retryBackoffMax)
		logNodeFailure(p.conf, err, backoff)
		timer.Reset(backoff)
	}
}

// fetchNode asks the panel for a node's config with a fresh client.
func fetchNode(ctx context.Context, nc *conf.NodeConfig) (*panel.Client, *panel.NodeInfo, error) {
	client, err := panel.New(nc)
	if err != nil {
		return nil, nil, err
	}
	info, err := client.GetNodeInfo(ctx)
	if err != nil {
		return nil, nil, err
	}
	if info == nil {
		return nil, nil, errNoNodeConfig
	}
	if nc.NodeID == 0 {
		log.WithField("api_host", nc.APIHost).Infof("Paired by key with panel node %d", info.Id)
	}
	return client, info, nil
}

// recoveredFor returns the config a retrier of n fetched for nc, if any.
func (n *Node) recoveredFor(nc conf.NodeConfig) (*panel.Client, *panel.NodeInfo) {
	if n == nil {
		return nil, nil
	}
	for _, p := range n.pending {
		if sameNode(p.conf, nc) {
			if client, info := p.recovered(); info != nil {
				return client, info
			}
		}
	}
	return nil, nil
}

// runningInfo returns the config n runs for nc, if any.
func (n *Node) runningInfo(nc conf.NodeConfig) *panel.NodeInfo {
	if n == nil {
		return nil
	}
	for i := range n.Configs {
		if sameNode(n.Configs[i], nc) {
			return n.currentInfo(i)
		}
	}
	return nil
}

// currentInfo is the config node i runs now: a controller may have applied a newer
// one in place since this generation started.
func (n *Node) currentInfo(i int) *panel.NodeInfo {
	if i < len(n.controllers) && n.controllers[i] != nil {
		if info := n.controllers[i].nodeInfo(); info != nil {
			return info
		}
	}
	return n.NodeInfos[i]
}

func sameNode(a, b conf.NodeConfig) bool {
	retry := func(p *int) int {
		if p == nil {
			return conf.DefaultNodeRetryCount
		}
		return *p
	}
	return a.APIHost == b.APIHost && a.NodeID == b.NodeID && a.Key == b.Key &&
		a.Timeout == b.Timeout && a.Protocol == b.Protocol && retry(a.RetryCount) == retry(b.RetryCount)
}

func nodeFields(nc conf.NodeConfig, err error) log.Fields {
	f := log.Fields{"api_host": nc.APIHost}
	if nc.NodeID > 0 {
		f["node_id"] = nc.NodeID
	} else {
		f["node_id"] = "pair by key"
	}
	if err != nil {
		f["err"] = err
	}
	return f
}

// logNodeFailure explains why a node has no config and when it retries.
func logNodeFailure(nc conf.NodeConfig, err error, retryIn time.Duration) {
	entry := log.WithFields(nodeFields(nc, err)).WithField("retry_in", retryIn)
	switch {
	case panel.IsUnauthorized(err):
		entry.Error("Panel rejected the node key; it may have been rotated or revoked, update ApiKey. Other nodes keep running")
	case errors.Is(err, panel.ErrNotPairingKey):
		entry.Error("Node has no NodeID and its key is the global server token; use the node's own key. Other nodes keep running")
	default:
		entry.Error("Cannot get node config from the panel, other nodes keep running")
	}
}

// Close stops the retries and closes every controller, even if some fail, and
// returns their errors.
func (n *Node) Close() error {
	if n.cancel != nil {
		n.cancel()
	}
	n.wg.Wait()
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

// Apply moves the running generation to the node set configs without
// restarting the core: nodes no longer listed are stopped and new ones are
// fetched and started. It only does so when that keeps the core's config
// (outbounds, routing, DNS) unchanged and no node is pending; otherwise, or
// when a new node's config cannot be fetched, it changes nothing and returns
// false, and the caller reloads the whole configuration instead. An error
// means nodes were changed but a new one failed to start; a reload repairs it.
func (n *Node) Apply(c *conf.Conf, configs []conf.NodeConfig, x *core.V2Core) (bool, error) {
	if len(n.pending) > 0 {
		return false, nil
	}
	keep := make([]bool, len(n.Configs))
	var added []conf.NodeConfig
	for _, nc := range configs {
		found := false
		for i := range n.Configs {
			if !keep[i] && sameNode(n.Configs[i], nc) {
				keep[i], found = true, true
				break
			}
		}
		if !found {
			added = append(added, nc)
		}
	}
	if len(added) == 0 && !slices.Contains(keep, false) {
		return true, nil
	}
	type fetched struct {
		client *panel.Client
		info   *panel.NodeInfo
	}
	fresh := make([]fetched, len(added))
	for i := range added {
		client, info, err := fetchNode(context.Background(), &added[i])
		if err != nil {
			log.WithFields(nodeFields(added[i], err)).Warn("Cannot fetch the config of a new node, reloading all nodes instead")
			return false, nil
		}
		fresh[i] = fetched{client: client, info: info}
	}
	var running, next []*panel.NodeInfo
	for i := range n.Configs {
		info := n.currentInfo(i)
		running = append(running, info)
		if keep[i] {
			next = append(next, info)
		}
	}
	for _, f := range fresh {
		next = append(next, f.info)
	}
	same, err := core.SameConfig(c, running, next)
	if err != nil || !same {
		// New routes, outbounds or egress rules need a new core.
		return false, nil
	}

	again := &Node{}
	var errs []error
	for i := range n.Configs {
		if keep[i] {
			again.Configs = append(again.Configs, n.Configs[i])
			again.NodeInfos = append(again.NodeInfos, n.NodeInfos[i])
			again.controllers = append(again.controllers, n.controllers[i])
			continue
		}
		log.WithFields(nodeFields(n.Configs[i], nil)).Info("Node removed from the panel's list, stopping it")
		if err := n.controllers[i].Close(); err != nil {
			errs = append(errs, fmt.Errorf("stop node %d: %w", n.Configs[i].NodeID, err))
		}
	}
	for i, f := range fresh {
		nc := added[i]
		controller := NewController(f.client, &nc, f.info)
		if err := controller.Start(x); err != nil {
			errs = append(errs, fmt.Errorf("start node %d: %w", nc.NodeID, err))
			continue
		}
		log.WithFields(nodeFields(nc, nil)).Info("Node added by the panel, started")
		again.Configs = append(again.Configs, nc)
		again.NodeInfos = append(again.NodeInfos, f.info)
		again.controllers = append(again.controllers, controller)
	}
	n.Configs, n.NodeInfos, n.controllers = again.Configs, again.NodeInfos, again.controllers
	return true, errors.Join(errs...)
}
