package panel

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The node state goes with config pulls on v3 and v1 once set, and the error
// only with "down".
func TestNodeStateIsSentOnConfigPulls(t *testing.T) {
	for _, protocol := range []string{"v3", "v1"} {
		t.Run(protocol, func(t *testing.T) {
			p := newFakePanel(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
				switch {
				case r.URL.Path == "/api/v3/server":
					_, _ = w.Write([]byte(`{"code":200,"data":{"protocol":"vless","server_port":443}}`))
				default:
					_, _ = w.Write([]byte(`{"protocol":"vless","server_port":443}`))
				}
			})
			c := newTestClient(t, p.URL, protocol)
			pull := func() recordedRequest {
				t.Helper()
				if _, err := c.GetNodeInfo(context.Background()); err != nil {
					t.Fatalf("node info: %v", err)
				}
				reqs := p.Requests()
				return reqs[len(reqs)-1]
			}
			if r := pull(); r.Header.Get(NodeStateHeader) != "" || r.Header.Get(NodeErrorHeader) != "" {
				t.Fatalf("state sent before the first apply: %v", r.Header)
			}
			c.SetNodeDown("apply node failed: port in use")
			r := pull()
			if r.Header.Get(NodeStateHeader) != "down" || r.Header.Get(NodeErrorHeader) != "apply node failed: port in use" {
				t.Fatalf("down: %q %q", r.Header.Get(NodeStateHeader), r.Header.Get(NodeErrorHeader))
			}
			c.SetNodeUp()
			r = pull()
			if r.Header.Get(NodeStateHeader) != "ok" || len(r.Header.Values(NodeErrorHeader)) != 0 {
				t.Fatalf("ok: %q %q", r.Header.Get(NodeStateHeader), r.Header.Get(NodeErrorHeader))
			}
			if !strings.Contains(r.Header.Get(FeaturesHeader), "node-state") {
				t.Fatalf("features %q", r.Header.Get(FeaturesHeader))
			}
		})
	}
}

// Other requests do not carry the node state.
func TestNodeStateIsOnlySentOnConfigPulls(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		_, _ = w.Write([]byte(`{"users":[]}`))
	})
	c := newTestClient(t, p.URL, "v1")
	c.SetNodeDown("apply node failed")
	if _, err := c.GetUserList(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := p.Requests()[0]; r.Header.Get(NodeStateHeader) != "" {
		t.Fatalf("user list carried the node state: %v", r.Header)
	}
}

func TestSetNodeDownSanitizesTheReason(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1", "v1")
	c.SetNodeDown("request cert failed: secret-token rejected\r\nX-Injected: 1\tby https://user:pass@acme.test/dir, key hunter22, ünïcode",
		"hunter22", "x")
	got := c.nodeError
	want := "request cert failed: [redacted] rejected X-Injected: 1 by https://[redacted]@acme.test/dir, key [redacted], n code"
	if got != want {
		t.Fatalf("reason\n got %q\nwant %q", got, want)
	}
}

func TestSanitizeNodeError(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"key-like", "bad private key aGVsbG8td29ybGQtdGhpcy1pcy1hLWtleS12YWx1ZQ end", "bad private key [redacted] end"},
		{"uuid", "user 00000000-0000-4000-8000-000000000001 invalid", "user [redacted] invalid"},
		{"control", "a\x00b\x1b[31mc\x7f", "a b [31mc"},
		{"blank", " \r\n\t ", ""},
		{"truncated", strings.Repeat("ab ", 100), strings.TrimSpace(strings.Repeat("ab ", 100)[:maxNodeErrorLen])},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeNodeError(tc.in)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if len(got) > maxNodeErrorLen {
				t.Fatalf("%d bytes", len(got))
			}
			for _, r := range got {
				if r < 0x20 || r > 0x7e {
					t.Fatalf("non-printable %q in %q", r, got)
				}
			}
		})
	}
}
