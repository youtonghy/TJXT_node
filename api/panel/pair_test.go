package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/youtonghy/TJXT_node/conf"
)

func newKeyOnlyClient(t *testing.T, url, protocol string, nodeID int) *Client {
	t.Helper()
	retry := 0
	c, err := New(&conf.NodeConfig{APIHost: url, NodeID: nodeID, Key: "node-key", Timeout: 5, RetryCount: &retry, Protocol: protocol})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

// configPanel answers the node config on both protocols with body.
func configPanel(t *testing.T, body string) *fakePanel {
	return newFakePanel(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch r.URL.Path {
		case "/api/v3/server":
			_, _ = w.Write([]byte(`{"data":` + body + `}`))
		case "/api/v2/server/config":
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	})
}

func TestPairByKeyLearnsNodeIDWithoutSendingOne(t *testing.T) {
	for _, protocol := range []string{"v3", "v1"} {
		t.Run(protocol, func(t *testing.T) {
			p := configPanel(t, `{"id":3,"protocol":"vless","server_port":443,"base_config":{"node_id":42}}`)
			c := newKeyOnlyClient(t, p.URL, protocol, 0)
			info, err := c.GetNodeInfo(t.Context())
			if err != nil {
				t.Fatalf("node info: %v", err)
			}
			if info.Id != 42 || c.EffectiveNodeID() != 42 || !strings.HasSuffix(info.Tag, ":42") {
				t.Fatalf("learned id: info=%d client=%d tag=%s", info.Id, c.EffectiveNodeID(), info.Tag)
			}
			if !strings.Contains(info.Common.CertInfo.CertFile, "vless42") {
				t.Fatalf("cert path uses the learned id: %s", info.Common.CertInfo.CertFile)
			}
			for _, r := range p.Requests() {
				q, _ := url.ParseQuery(r.Query)
				if q.Has("node_id") || q.Has("server_id") {
					t.Errorf("%s sent a node id in the query: %s", r.Path, r.Query)
				}
				if r.Path == "/api/v3/server" {
					var env struct {
						Params map[string]any `json:"params"`
					}
					if err := json.Unmarshal(r.Body, &env); err != nil {
						t.Fatal(err)
					}
					if _, ok := env.Params["node_id"]; ok {
						t.Errorf("gateway params carry node_id: %v", env.Params)
					}
					if _, ok := env.Params["server_id"]; ok {
						t.Errorf("gateway params carry server_id: %v", env.Params)
					}
				}
			}
		})
	}
}

func TestPairByKeyRejectsGlobalToken(t *testing.T) {
	p := configPanel(t, `{"id":3,"protocol":"vless","server_port":443,"base_config":{"push_interval":60}}`)
	c := newKeyOnlyClient(t, p.URL, "v3", 0)
	for range 2 {
		// A rejected config must not read as "unchanged" on the next fetch.
		if _, err := c.GetNodeInfo(t.Context()); !errors.Is(err, ErrNotPairingKey) {
			t.Fatalf("want ErrNotPairingKey, got %v", err)
		}
	}
}

func TestConfiguredNodeIDMustMatchPanel(t *testing.T) {
	p := configPanel(t, `{"protocol":"vless","server_port":443,"base_config":{"node_id":9}}`)
	c := newKeyOnlyClient(t, p.URL, "v1", 7)
	if _, err := c.GetNodeInfo(t.Context()); err == nil || !strings.Contains(err.Error(), "node 9") {
		t.Fatalf("want a node id mismatch error, got %v", err)
	}
}

func TestLegacyNodeIDStillSentAndAccepted(t *testing.T) {
	for _, protocol := range []string{"v3", "v1"} {
		t.Run(protocol, func(t *testing.T) {
			p := configPanel(t, `{"protocol":"vless","server_port":443}`)
			c := newKeyOnlyClient(t, p.URL, protocol, 7)
			info, err := c.GetNodeInfo(t.Context())
			if err != nil || info.Id != 7 {
				t.Fatalf("legacy config: info=%+v err=%v", info, err)
			}
			r := p.Requests()[0]
			if protocol == "v1" {
				if q, _ := url.ParseQuery(r.Query); q.Get("node_id") != "7" {
					t.Fatalf("legacy query lacks node_id: %s", r.Query)
				}
			} else if !strings.Contains(string(r.Body), `"node_id":7`) || !strings.Contains(string(r.Body), `"server_id":7`) {
				t.Fatalf("legacy gateway params lack the node id: %s", r.Body)
			}
		})
	}
}

func TestNodeUnauthorizedIsAnAuthError(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"bad token","error_code":"node_unauthorized"}`))
	})
	c := newKeyOnlyClient(t, p.URL, "v3", 0)
	_, err := c.GetNodeInfo(t.Context())
	if !IsUnauthorized(err) {
		t.Fatalf("want an auth error, got %v", err)
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Permanent() {
		t.Fatalf("a rotated key must not drop traffic batches as permanently rejected: %v", err)
	}
	if !IsUnauthorized(&StatusError{Code: http.StatusForbidden}) || IsUnauthorized(&StatusError{Code: http.StatusBadRequest}) {
		t.Fatal("401/403 are auth errors, a plain 400 is not")
	}
}
