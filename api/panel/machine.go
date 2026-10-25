package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// MachineInfo is the panel's answer to a machine key: the nodes this agent runs.
type MachineInfo struct {
	ID   int    `json:"machine_id"`
	Name string `json:"name"`
	// PullInterval is in seconds; the machine polls its node list this often.
	PullInterval int           `json:"pull_interval"`
	Nodes        []MachineNode `json:"nodes"`
}

// MachineNode is one node assigned to the machine.
type MachineNode struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
}

// NodeIDs returns the ids of the machine's nodes in the panel's order.
func (m *MachineInfo) NodeIDs() []int {
	ids := make([]int, 0, len(m.Nodes))
	for _, n := range m.Nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

// Interval returns how often to poll the node list.
func (m *MachineInfo) Interval() time.Duration {
	if m.PullInterval <= 0 {
		return time.Minute
	}
	return time.Duration(m.PullInterval) * time.Second
}

// ErrMachineUnsupported means the panel has no machine endpoint: it predates
// machine pairing, so the key can only be a node key.
var ErrMachineUnsupported = errors.New("the panel does not support machine pairing")

const machineEndpoint = "server/UniProxy/machine"

// GetMachine fetches the machine's node list with the machine key. It returns
// nil when the list is unchanged since the last fetch (304).
func (c *Client) GetMachine(ctx context.Context) (*MachineInfo, error) {
	if c.v3Available() {
		r, data, err := c.callV3(ctx, "GET", machineEndpoint, map[string]any{}, c.getMachineEtag())
		if err == nil {
			if r.StatusCode() == http.StatusNotModified {
				return nil, nil
			}
			return c.acceptMachine(r.Header().Get("ETag"), data)
		}
		if !errors.Is(err, errV3Unsupported) {
			return nil, err
		}
	}
	if !c.useV1() {
		return nil, ErrMachineUnsupported
	}
	r, err := c.client.R().
		SetContext(ctx).
		SetHeader("If-None-Match", c.getMachineEtag()).
		ForceContentType("application/json").
		Get("/api/v1/" + machineEndpoint)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("received nil response")
	}
	if r.StatusCode() == http.StatusNotModified {
		return nil, nil
	}
	if r.StatusCode() == http.StatusNotFound {
		return nil, ErrMachineUnsupported
	}
	if err := checkResponse(r, nil, "get machine"); err != nil {
		return nil, err
	}
	return c.acceptMachine(r.Header().Get("ETag"), r.Body())
}

func (c *Client) acceptMachine(etag string, body []byte) (*MachineInfo, error) {
	if !json.Valid(body) {
		return nil, ErrNotNodeAPI
	}
	m := &MachineInfo{}
	if err := json.Unmarshal(body, m); err != nil {
		return nil, fmt.Errorf("decode machine: %w", err)
	}
	if m.ID <= 0 {
		return nil, fmt.Errorf("panel answered without a machine id")
	}
	c.mu.Lock()
	c.machineEtag = etag
	c.mu.Unlock()
	return m, nil
}

func (c *Client) getMachineEtag() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.machineEtag
}
