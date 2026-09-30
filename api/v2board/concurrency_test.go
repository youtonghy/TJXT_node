package panel

import (
	"context"
	"net/http"
	"sync"
	"testing"
)

// TestClientConcurrentUse runs the monitor and report calls of one client in
// parallel while auto mode falls back from v3 to v1; run it with -race.
func TestClientConcurrentUse(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch r.URL.Path {
		case "/api/v3/server":
			w.WriteHeader(http.StatusNotFound)
		case "/api/v2/server/config":
			w.Header().Set("ETag", `"c"`)
			_, _ = w.Write([]byte(`{"protocol":"vless","server_port":443}`))
		case "/api/v1/server/UniProxy/user":
			w.Header().Set("ETag", `"u"`)
			_, _ = w.Write([]byte(`{"users":[{"id":1,"uuid":"a"}]}`))
		case "/api/v1/server/UniProxy/alivelist":
			_, _ = w.Write([]byte(`{"alive":{"1":1}}`))
		default:
			_, _ = w.Write([]byte(`{"data":true}`))
		}
	})
	c := newTestClient(t, p.URL, "auto")
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(4)
		go func() {
			defer wg.Done()
			if _, err := c.GetNodeInfo(ctx); err != nil {
				t.Errorf("node info: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := c.GetUserList(ctx); err != nil {
				t.Errorf("user list: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := c.GetUserAlive(ctx); err != nil {
				t.Errorf("alive: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := c.ReportUserTraffic(ctx, "b", []UserTraffic{{UID: 1, Upload: 1}}); err != nil {
				t.Errorf("push: %v", err)
			}
		}()
	}
	wg.Wait()
	if c.v3Available() {
		t.Fatal("auto mode must remember that v3 is unavailable")
	}
}
