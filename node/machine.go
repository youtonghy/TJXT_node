package node

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/conf"
)

// Machine follows the node list of a machine pairing (conf.Machine): the panel
// assigns nodes to the machine, and the agent runs each of them with the
// machine key. It lives across generations of nodes; a changed list is
// announced with the notify callback of Run.
type Machine struct {
	conf   conf.MachineConfig
	client *panel.Client

	mu sync.Mutex
	// ids is the last node list the panel sent; known is false before the
	// first one arrived.
	ids      []int
	known    bool
	interval time.Duration
}

// NewMachine returns a machine for c that has not fetched its list yet.
func NewMachine(c conf.MachineConfig) (*Machine, error) {
	client, err := panel.New(new(c.Client()))
	if err != nil {
		return nil, err
	}
	return &Machine{conf: c, client: client, interval: time.Minute}, nil
}

// Matches reports whether the machine connects as c does.
func (m *Machine) Matches(c *conf.MachineConfig) bool {
	return conf.SameMachine(&m.conf, c)
}

// Refresh fetches the node list and reports whether it changed. On an error
// the last known list stays in use.
func (m *Machine) Refresh(ctx context.Context) (bool, error) {
	info, err := m.client.GetMachine(ctx)
	if err != nil {
		return false, err
	}
	if info == nil {
		// Unchanged (304).
		return false, nil
	}
	ids := info.NodeIDs()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.interval = info.Interval()
	changed := !m.known || !slices.Equal(m.ids, ids)
	if changed {
		log.WithFields(log.Fields{"machine": info.ID, "name": info.Name, "nodes": ids}).Info("Machine node list received")
	}
	m.ids, m.known = ids, true
	return changed, nil
}

// NodeConfigs returns the settings of the nodes the machine runs, in the
// panel's order; none before the first list arrived.
func (m *Machine) NodeConfigs() []conf.NodeConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	configs := make([]conf.NodeConfig, 0, len(m.ids))
	for _, id := range m.ids {
		configs = append(configs, m.conf.Node(id))
	}
	return configs
}

func (m *Machine) pollInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.interval
}

// Run polls the node list until ctx ends and calls notify after each change.
// Failures are logged and retried on the next poll; the running nodes stay.
func (m *Machine) Run(ctx context.Context, notify func()) {
	timer := time.NewTimer(m.pollInterval())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		changed, err := m.Refresh(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			m.logFailure(err)
		case changed:
			notify()
		}
		timer.Reset(m.pollInterval())
	}
}

// logFailure explains why the node list could not be fetched.
func (m *Machine) logFailure(err error) {
	entry := log.WithFields(log.Fields{"api_host": m.conf.APIHost, "err": err, "retry_in": m.pollInterval()})
	switch {
	case panel.IsUnauthorized(err):
		entry.Error("Panel rejected the machine key; it may have been rotated, reinstall with the new command. Running nodes keep running")
	case errors.Is(err, panel.ErrMachineUnsupported):
		entry.Error("Panel does not support machine pairing; upgrade the panel, or pair each node with its own key")
	default:
		entry.Error("Cannot get the machine's node list from the panel, running nodes keep running")
	}
}

// Desired returns the nodes to run: the configured Nodes followed by the
// machine's. A configured node the machine runs as well (same panel and id) is
// left out, so it runs once, as the machine's.
func Desired(nodes []conf.NodeConfig, m *Machine) []conf.NodeConfig {
	if m == nil {
		return nodes
	}
	machineNodes := m.NodeConfigs()
	desired := make([]conf.NodeConfig, 0, len(nodes)+len(machineNodes))
	for _, n := range nodes {
		dup := n.NodeID > 0 && slices.ContainsFunc(machineNodes, func(mn conf.NodeConfig) bool {
			return mn.NodeID == n.NodeID && conf.SameAPIHost(mn.APIHost, n.APIHost)
		})
		if !dup {
			desired = append(desired, n)
		}
	}
	return append(desired, machineNodes...)
}
