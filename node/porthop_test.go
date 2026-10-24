package node

import (
	"strings"
	"testing"

	"github.com/youtonghy/TJXT_node/api/panel"
)

func TestHopPortSetAcceptsPortsAndRangesOnly(t *testing.T) {
	if set, err := hopPortSet(" 20000-30000, 443 "); err != nil || set != "20000-30000, 443" {
		t.Fatalf("set %q err %v", set, err)
	}
	for _, bad := range []string{"", "0-10", "1-70000", "a-b", "443;drop"} {
		if _, err := hopPortSet(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

// The redirect is installed when the inbound starts with hop ports and removed
// when it stops; each node has its own table.
func TestPortHopRedirectFollowsTheInbound(t *testing.T) {
	var scripts []string
	defer func(supported bool, run func(string) error) {
		portHopSupported, runNft = supported, run
	}(portHopSupported, runNft)
	portHopSupported = true
	runNft = func(script string) error {
		scripts = append(scripts, script)
		return nil
	}
	ctrl := &Controller{tag: "[panel]-hysteria2:7", info: &panel.NodeInfo{
		Type:   "hysteria2",
		Common: &panel.CommonNode{ServerPort: 443, HopPorts: "20000-30000"},
	}}
	ctrl.syncPortHop(true)
	ctrl.syncPortHop(false)
	ctrl.syncPortHop(false)
	if len(scripts) != 2 {
		t.Fatalf("scripts %q", scripts)
	}
	table := hopTable(ctrl.tag)
	if !strings.Contains(scripts[0], "table inet "+table+" {") ||
		!strings.Contains(scripts[0], "udp dport { 20000-30000 } redirect to :443") {
		t.Fatalf("install script %q", scripts[0])
	}
	if !strings.Contains(scripts[1], "delete table inet "+table) {
		t.Fatalf("remove script %q", scripts[1])
	}
	if hopTable("[panel]-hysteria2:8") == table {
		t.Fatal("nodes share a table")
	}
}
