package cmd

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/youtonghy/TJXT_node/conf"
)

const testKey = "node-secret-key-1234"

// pairPanel answers the v1 config endpoint with status and body and records
// the queries it saw.
func pairPanel(t *testing.T, status int, body string) (*httptest.Server, *[]string) {
	t.Helper()
	var queries []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/server/config" {
			http.NotFound(w, r)
			return
		}
		queries = append(queries, r.URL.RawQuery)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s, &queries
}

func testPairOpts(host, path string) pairOptions {
	return pairOptions{APIHost: host, Key: testKey, ConfigPath: path, Protocol: "v1", Timeout: 5}
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("error without exit code: %v", err)
	}
	return ee.code
}

func TestPairWritesConfigWithLearnedNodeID(t *testing.T) {
	s, queries := pairPanel(t, http.StatusOK, `{"protocol":"vless","name":"HK-1","server_port":443,"base_config":{"node_id":42}}`)
	path := filepath.Join(t.TempDir(), "tjxt", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"Log":{"Level":"debug"},"Nodes":[{"ApiHost":"http://old","ApiKey":"o","NodeID":3}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runPair(t.Context(), testPairOpts(s.URL+"/", path), &out); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if strings.Contains(out.String(), testKey) {
		t.Fatalf("output leaks the key: %s", out.String())
	}
	if !strings.Contains(out.String(), "node 42") || !strings.Contains(out.String(), "vless") || !strings.Contains(out.String(), "HK-1") || !strings.Contains(out.String(), "Node port 443") {
		t.Fatalf("output lacks the node: %s", out.String())
	}
	if strings.Contains((*queries)[0], "node_id") {
		t.Fatalf("pair sent a node id: %s", (*queries)[0])
	}
	c := conf.New()
	if err := c.LoadFromPath(path); err != nil {
		t.Fatal(err)
	}
	n := c.NodeConfigs[0]
	if len(c.NodeConfigs) != 1 || n.NodeID != 42 || n.Key != testKey || n.APIHost != s.URL || n.Protocol != "v1" || n.Timeout != 5 {
		t.Fatalf("written node: %+v", c.NodeConfigs)
	}
	if c.LogConfig.Level != "debug" {
		t.Fatalf("Log not kept: %+v", c.LogConfig)
	}
}

func TestPairDryRunDoesNotWrite(t *testing.T) {
	s, _ := pairPanel(t, http.StatusOK, `{"protocol":"vless","server_port":443,"base_config":{"node_id":42}}`)
	path := filepath.Join(t.TempDir(), "config.json")
	o := testPairOpts(s.URL, path)
	o.DryRun = true
	if err := runPair(t.Context(), o, &bytes.Buffer{}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote the config: %v", err)
	}
}

func TestPairFailures(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	cases := []struct {
		name   string
		status int
		body   string
		host   string
		code   int
	}{
		{name: "401", status: http.StatusUnauthorized, body: `{"message":"no"}`, code: exitPairBadKey},
		{name: "403", status: http.StatusForbidden, body: `{"message":"no"}`, code: exitPairBadKey},
		{name: "node_unauthorized", status: http.StatusBadRequest, body: `{"message":"token is error","error_code":"node_unauthorized"}`, code: exitPairBadKey},
		{name: "global token", status: http.StatusOK, body: `{"id":3,"protocol":"vless","server_port":443}`, code: exitPairGlobalToken},
		{name: "unreachable", host: closedURL, code: exitPairUnreachable},
		{name: "invalid url", host: "panel.example.com", code: exitPairUsage},
		{name: "ftp url", host: "ftp://panel.example.com", code: exitPairUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := tc.host
			if host == "" {
				s, _ := pairPanel(t, tc.status, tc.body)
				host = s.URL
			}
			path := filepath.Join(t.TempDir(), "config.json")
			var out bytes.Buffer
			err := runPair(t.Context(), testPairOpts(host, path), &out)
			if got := exitCode(t, err); got != tc.code {
				t.Fatalf("exit code %d, want %d (%v)", got, tc.code, err)
			}
			if strings.Contains(out.String()+err.Error(), testKey) {
				t.Fatal("the key leaked into the output")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("a failed pairing wrote the config")
			}
		})
	}
}

func TestPairRefusesMultiNodeConfig(t *testing.T) {
	s, _ := pairPanel(t, http.StatusOK, `{"protocol":"vless","server_port":443,"base_config":{"node_id":42}}`)
	path := filepath.Join(t.TempDir(), "config.json")
	multi := `{"Nodes":[{"ApiHost":"http://a","ApiKey":"1"},{"ApiHost":"http://b","ApiKey":"2"}]}`
	if err := os.WriteFile(path, []byte(multi), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := exitCode(t, runPair(t.Context(), testPairOpts(s.URL, path), &bytes.Buffer{})); code != exitPairConfig {
		t.Fatalf("exit code %d, want %d", code, exitPairConfig)
	}
	o := testPairOpts(s.URL, path)
	o.Force = true
	if err := runPair(t.Context(), o, &bytes.Buffer{}); err != nil {
		t.Fatalf("forced pair: %v", err)
	}
}
