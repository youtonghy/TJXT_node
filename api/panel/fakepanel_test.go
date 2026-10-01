package panel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/youtonghy/TJXT_node/conf"
)

// recordedRequest is one request seen by the fake panel.
type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// fakePanel is an httptest panel that records requests and answers through a
// per-test handler.
type fakePanel struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	handle   func(w http.ResponseWriter, r *http.Request, body []byte)
}

func newFakePanel(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body []byte)) *fakePanel {
	t.Helper()
	p := &fakePanel{handle: handle}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		p.mu.Lock()
		p.requests = append(p.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Header: r.Header.Clone(),
			Body:   body,
		})
		p.mu.Unlock()
		p.handle(w, r, body)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *fakePanel) Requests() []recordedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedRequest(nil), p.requests...)
}

func newTestClient(t *testing.T, url, protocol string) *Client {
	t.Helper()
	retry := 0
	c, err := New(&conf.NodeConfig{
		APIHost:    url,
		NodeID:     7,
		Key:        "secret-token",
		Timeout:    5,
		RetryCount: &retry,
		Protocol:   protocol,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}
