package panel

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestTokenOnlyInHeader drives every panel call over v3 and v1 and asserts the
// node key appears only in the X-Server-Token header.
func TestTokenOnlyInHeader(t *testing.T) {
	for _, protocol := range []string{"v3", "v1"} {
		t.Run(protocol, func(t *testing.T) {
			p := newFakePanel(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
				switch {
				case r.URL.Path == "/api/v3/server":
					_, _ = w.Write([]byte(`{"code":200,"data":{"protocol":"vless","server_port":443,"users":[]}}`))
				case strings.HasSuffix(r.URL.Path, "/config"):
					_, _ = w.Write([]byte(`{"protocol":"vless","server_port":443}`))
				case strings.HasSuffix(r.URL.Path, "/user"):
					_, _ = w.Write([]byte(`{"users":[]}`))
				default:
					_, _ = w.Write([]byte(`{"data":true}`))
				}
			})
			c := newTestClient(t, p.URL, protocol)
			ctx := context.Background()
			if _, err := c.GetNodeInfo(ctx); err != nil {
				t.Fatalf("node info: %v", err)
			}
			if _, err := c.GetUserList(ctx); err != nil {
				t.Fatalf("user list: %v", err)
			}
			if _, err := c.ReportUserTraffic(ctx, "b", []UserTraffic{{UID: 1, Upload: 1}}); err != nil {
				t.Fatalf("push: %v", err)
			}
			online := map[int][]string{1: {"1.1.1.1"}}
			if err := c.ReportNodeOnlineUsers(ctx, &online); err != nil {
				t.Fatalf("alive: %v", err)
			}
			reqs := p.Requests()
			if len(reqs) < 4 {
				t.Fatalf("expected at least 4 requests, got %d", len(reqs))
			}
			for _, r := range reqs {
				if r.Header.Get("X-Server-Token") != "secret-token" {
					t.Errorf("%s %s: missing X-Server-Token header", r.Method, r.Path)
				}
				if strings.Contains(r.Query, "secret-token") || strings.Contains(r.Query, "token=") {
					t.Errorf("%s %s: token in query %q", r.Method, r.Path, r.Query)
				}
				if bytes.Contains(r.Body, []byte("secret-token")) || bytes.Contains(r.Body, []byte(`"token"`)) {
					t.Errorf("%s %s: token in body %s", r.Method, r.Path, r.Body)
				}
			}
		})
	}
}

// Every request names the agent, its Xray core and its features.
func TestRequestsIdentifyTheAgent(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		_, _ = w.Write([]byte(`{"users":[]}`))
	})
	c := newTestClient(t, p.URL, "v1")
	if _, err := c.GetUserList(context.Background()); err != nil {
		t.Fatal(err)
	}
	req := p.Requests()[0]
	if !strings.HasPrefix(req.Header.Get("User-Agent"), "tjxt-node/") ||
		!strings.Contains(req.Header.Get("User-Agent"), " xray/") {
		t.Fatalf("user agent %q", req.Header.Get("User-Agent"))
	}
	if !strings.Contains(req.Header.Get(FeaturesHeader), "revoked-users") {
		t.Fatalf("features %q", req.Header.Get(FeaturesHeader))
	}
}

// Once set, the certificate fingerprint goes with every request.
func TestCertFingerprintIsSentOnceSet(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		_, _ = w.Write([]byte(`{"users":[]}`))
	})
	c := newTestClient(t, p.URL, "v1")
	if _, err := c.GetUserList(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.SetCertFingerprint("ab12")
	if _, err := c.GetUserList(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := p.Requests()
	if reqs[0].Header.Get(CertFingerprintHeader) != "" || reqs[1].Header.Get(CertFingerprintHeader) != "ab12" {
		t.Fatalf("headers %q %q", reqs[0].Header.Get(CertFingerprintHeader), reqs[1].Header.Get(CertFingerprintHeader))
	}
}
