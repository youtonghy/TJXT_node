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

func TestParseNodeInfoAcceptsNumericSwitches(t *testing.T) {
	c := &Client{APIHost: "https://panel.example.com", NodeId: 1}
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`1`, true}, {`0`, false}, {`true`, true}, {`false`, false},
		{`"1"`, true}, {`"0"`, false}, {`null`, false},
	} {
		body := `{"protocol":"tuic","server_port":443,"zero_rtt_handshake":` + tc.raw +
			`,"ignore_client_bandwidth":` + tc.raw + `}`
		info, err := parseNodeInfo(c, []byte(body))
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if bool(info.Common.ZeroRTTHandshake) != tc.want || bool(info.Common.Ignore_Client_Bandwidth) != tc.want {
			t.Fatalf("%s: got %v/%v, want %v", tc.raw,
				info.Common.ZeroRTTHandshake, info.Common.Ignore_Client_Bandwidth, tc.want)
		}
	}
	if _, err := parseNodeInfo(c, []byte(`{"protocol":"tuic","server_port":443,"zero_rtt_handshake":"yes"}`)); err == nil {
		t.Fatal("expected an error for an unknown switch value")
	}
}
