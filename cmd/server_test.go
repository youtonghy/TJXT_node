package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/youtonghy/TJXT_node/conf"
	"github.com/youtonghy/TJXT_node/limiter"
)

// The process must come up even when no node gets its config: the core runs
// without inbounds and the pending nodes retry.
func TestStartWithoutAnyNodeConfig(t *testing.T) {
	limiter.Init()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer s.Close()
	retry := 0
	c := conf.New()
	c.LogConfig.Level = "error"
	c.NodeConfigs = []conf.NodeConfig{{APIHost: s.URL, NodeID: 1, Key: "k", Timeout: 5, RetryCount: &retry, Protocol: "v1"}}
	p, err := prepareConf(c, c.NodeConfigs, nil)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(p.nodes.NodeInfos) != 0 || p.nodes.Pending() != 1 {
		t.Fatalf("a 304 first answer must leave the node pending, not pass nil info: running=%d", len(p.nodes.NodeInfos))
	}
	inst, err := p.start(make(chan struct{}, 1))
	if err != nil {
		t.Fatalf("start without nodes: %v", err)
	}
	inst.close()
}
