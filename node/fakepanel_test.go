package node

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/conf"
	"github.com/youtonghy/TJXT_node/core"
	"github.com/youtonghy/TJXT_node/limiter"
)

type pushRecord struct {
	BatchID string
	Header  string
	Traffic map[string][2]int64
}

// fakePanel serves the v1 node endpoints from mutable state.
type fakePanel struct {
	*httptest.Server
	mu          sync.Mutex
	config      map[string]any
	users       []panel.UserInfo
	pushStatus  int
	userFetches int
	notModified int
	pushes      []pushRecord
	// down makes every endpoint answer 503, like a panel being deployed.
	down          bool
	configFetches int
	// revoked is sent as revoked_user_ids in push answers.
	revoked []int
}

func newFakePanel(t *testing.T, protocol string, users []panel.UserInfo) *fakePanel {
	t.Helper()
	p := &fakePanel{
		config: map[string]any{
			"protocol":    protocol,
			"listen_ip":   "127.0.0.1",
			"server_port": freePort(t),
			"base_config": map[string]any{"push_interval": 3600, "pull_interval": 3600},
		},
		users:      users,
		pushStatus: http.StatusOK,
	}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

func (p *fakePanel) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if p.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	switch r.URL.Path {
	case "/api/v3/server":
		w.WriteHeader(http.StatusNotFound)
	case "/api/v2/server/config":
		p.configFetches++
		_ = json.NewEncoder(w).Encode(p.config)
	case "/api/v1/server/UniProxy/user":
		p.userFetches++
		users := p.users
		if users == nil {
			users = []panel.UserInfo{}
		}
		body, _ := json.Marshal(map[string]any{"users": users, "class": "users"})
		etag := fmt.Sprintf("\"%x\"", sha256.Sum256(body))
		if r.Header.Get("If-None-Match") == etag {
			p.notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = w.Write(body)
	case "/api/v1/server/UniProxy/alivelist":
		_, _ = w.Write([]byte(`{"alive":{}}`))
	case "/api/v1/server/UniProxy/alive":
		_, _ = w.Write([]byte(`{"data":true}`))
	case "/api/v1/server/UniProxy/push":
		body, _ := io.ReadAll(r.Body)
		var rec pushRecord
		var payload struct {
			BatchID string              `json:"batch_id"`
			Traffic map[string][2]int64 `json:"traffic"`
		}
		_ = json.Unmarshal(body, &payload)
		rec.BatchID, rec.Traffic = payload.BatchID, payload.Traffic
		rec.Header = r.Header.Get(panel.TrafficBatchHeader)
		p.pushes = append(p.pushes, rec)
		w.WriteHeader(p.pushStatus)
		answer, _ := json.Marshal(map[string]any{
			"data": true, "rejected_user_ids": []int{}, "retry_safe": true,
			"revoked_user_ids": p.revoked,
		})
		_, _ = w.Write(answer)
	default:
		http.NotFound(w, r)
	}
}

func (p *fakePanel) setRevoked(ids []int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revoked = ids
}

func (p *fakePanel) setUsers(users []panel.UserInfo) {
	p.mu.Lock()
	p.users = users
	p.mu.Unlock()
}

func (p *fakePanel) setDown(down bool) {
	p.mu.Lock()
	p.down = down
	p.mu.Unlock()
}

func (p *fakePanel) ConfigFetches() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.configFetches
}

func (p *fakePanel) setPushStatus(status int) {
	p.mu.Lock()
	p.pushStatus = status
	p.mu.Unlock()
}

func (p *fakePanel) NotModified() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.notModified
}

func (p *fakePanel) Pushes() []pushRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pushRecord(nil), p.pushes...)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func testUsers(ids ...int) []panel.UserInfo {
	users := make([]panel.UserInfo, 0, len(ids))
	for _, id := range ids {
		users = append(users, panel.UserInfo{Id: id, Uuid: fmt.Sprintf("00000000-0000-4000-8000-%012d", id)})
	}
	return users
}

// startTestController starts a real Xray core and one controller against p.
func startTestController(t *testing.T, p *fakePanel) (*Controller, *core.V2Core) {
	t.Helper()
	limiter.Init()
	retry := 0
	nc := &conf.NodeConfig{APIHost: p.URL, NodeID: 1, Key: "k", Timeout: 5, RetryCount: &retry, Protocol: "v1"}
	api, err := panel.New(nc)
	if err != nil {
		t.Fatalf("panel client: %v", err)
	}
	info, err := api.GetNodeInfo(t.Context())
	if err != nil {
		t.Fatalf("node info: %v", err)
	}
	c := conf.New()
	c.LogConfig.Level = "error"
	x := core.New(c)
	x.ReloadCh = make(chan struct{}, 1)
	if err := x.Start([]*panel.NodeInfo{info}); err != nil {
		t.Fatalf("start core: %v", err)
	}
	ctrl := NewController(api, nc, info)
	if err := ctrl.Start(x); err != nil {
		_ = x.Close()
		t.Fatalf("start controller: %v", err)
	}
	t.Cleanup(func() {
		if err := ctrl.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := x.Close(); err != nil {
			t.Errorf("close core: %v", err)
		}
	})
	return ctrl, x
}
