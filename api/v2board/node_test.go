package panel

import (
	"strings"
	"testing"
)

func TestParseNodeInfoRejectsRetiredMTProto(t *testing.T) {
	c := &Client{APIHost: "https://panel.example.com", NodeId: 1}
	_, err := parseNodeInfo(c, []byte(`{"protocol":"mtproto","server_port":443}`))
	if err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("expected retired error, got %v", err)
	}
	info, err := parseNodeInfo(c, []byte(`{"protocol":"vless","server_port":443}`))
	if err != nil || info.Type != "vless" {
		t.Fatalf("vless must still parse: %+v %v", info, err)
	}
}
