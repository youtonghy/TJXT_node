package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/youtonghy/TJXT_node/conf"
)

func newMachineClient(t *testing.T, url string) *Client {
	t.Helper()
	retry := 0
	c, err := New(&conf.NodeConfig{APIHost: url, Key: "machine-token", Timeout: 5, RetryCount: &retry, Protocol: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const machineBody = `{"machine_id":3,"name":"Tokyo","pull_interval":30,"nodes":[{"id":5,"name":"A","protocol":"vless"},{"id":6,"name":"B","protocol":"hysteria2"}]}`

func TestMachineThroughTheGatewayWithETag(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req struct {
			Endpoint string `json:"endpoint"`
		}
		_ = json.Unmarshal(body, &req)
		if r.URL.Path != "/api/v3/server" || req.Endpoint != "server/UniProxy/machine" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") == `"m1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"m1"`)
		_, _ = w.Write([]byte(`{"code":200,"data":` + machineBody + `}`))
	})
	c := newMachineClient(t, p.URL)
	m, err := c.GetMachine(t.Context())
	if err != nil || m == nil {
		t.Fatalf("machine: %v %v", m, err)
	}
	if m.ID != 3 || !slices.Equal(m.NodeIDs(), []int{5, 6}) || m.Interval().Seconds() != 30 {
		t.Fatalf("machine = %+v", m)
	}
	if again, err := c.GetMachine(t.Context()); err != nil || again != nil {
		t.Fatalf("an unchanged list must read as nil: %v %v", again, err)
	}
	for _, req := range p.Requests() {
		if req.Header.Get("X-Server-Token") != "machine-token" || req.Query != "node_type=v3ui" {
			t.Fatalf("machine request identifies a node or leaks the key: %+v", req)
		}
	}
}

func TestMachineFallsBackToV1AndDetectsOldPanels(t *testing.T) {
	var served atomic.Bool
	served.Store(true)
	p := newFakePanel(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if served.Load() && r.URL.Path == "/api/v1/server/UniProxy/machine" {
			_, _ = w.Write([]byte(machineBody))
			return
		}
		http.NotFound(w, r)
	})
	if m, err := newMachineClient(t, p.URL).GetMachine(t.Context()); err != nil || m == nil || m.Name != "Tokyo" {
		t.Fatalf("v1 machine: %v %v", m, err)
	}
	served.Store(false)
	if _, err := newMachineClient(t, p.URL).GetMachine(t.Context()); !errors.Is(err, ErrMachineUnsupported) {
		t.Fatalf("a panel without machines: %v", err)
	}
}
