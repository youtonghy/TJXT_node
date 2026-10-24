package panel

import (
	"encoding/json"
	"errors"
	"reflect"
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

func TestFlexIntAndFlexString(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: `100`, want: 100}, {raw: `"100"`, want: 100}, {raw: `" 7 "`, want: 7},
		{raw: `""`, want: 0}, {raw: `null`, want: 0}, {raw: `2.0`, want: 2},
		{raw: `1.5`, wantErr: true}, {raw: `"abc"`, wantErr: true}, {raw: `true`, wantErr: true},
	} {
		var n FlexInt
		err := json.Unmarshal([]byte(tc.raw), &n)
		if (err != nil) != tc.wantErr || (!tc.wantErr && int(n) != tc.want) {
			t.Fatalf("FlexInt %s: got %d, %v", tc.raw, n, err)
		}
	}
	for _, tc := range []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: `"443"`, want: "443"}, {raw: `443`, want: "443"}, {raw: `null`, want: ""},
		{raw: `""`, want: ""}, {raw: `true`, wantErr: true}, {raw: `[1]`, wantErr: true},
	} {
		var s FlexString
		err := json.Unmarshal([]byte(tc.raw), &s)
		if (err != nil) != tc.wantErr || (!tc.wantErr && string(s) != tc.want) {
			t.Fatalf("FlexString %s: got %q, %v", tc.raw, s, err)
		}
	}
}

func TestEnvMap(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    map[string]string
		wantErr bool
	}{
		{raw: `"CF_DNS_API_TOKEN=abc, CF_ZONE=x=y"`, want: map[string]string{"CF_DNS_API_TOKEN": "abc", "CF_ZONE": "x=y"}},
		{raw: `{"CF_DNS_API_TOKEN":"abc","TTL":60}`, want: map[string]string{"CF_DNS_API_TOKEN": "abc", "TTL": "60"}},
		{raw: `""`, want: map[string]string{}},
		{raw: `null`, want: nil},
		{raw: `"novalue"`, wantErr: true},
		{raw: `{"A":{"b":1}}`, wantErr: true},
		{raw: `[1]`, wantErr: true},
	} {
		var m EnvMap
		err := json.Unmarshal([]byte(tc.raw), &m)
		if (err != nil) != tc.wantErr {
			t.Fatalf("EnvMap %s: %v", tc.raw, err)
		}
		if !tc.wantErr && !reflect.DeepEqual(map[string]string(m), tc.want) {
			t.Fatalf("EnvMap %s: got %v, want %v", tc.raw, m, tc.want)
		}
	}
}

// Old panels send TLS settings as strings, new ones as numbers, booleans or
// objects; both parse to the same node config.
func TestParseNodeInfoAcceptsOldAndNewTLSSettings(t *testing.T) {
	c := &Client{APIHost: "https://panel.example.com", NodeId: 1}
	for _, body := range []string{
		`{"protocol":"vless","tls":2,"up_mbps":"100","down_mbps":"",
		  "tls_settings":{"server_port":"8443","xver":"1","reject_unknown_sni":"1",
		  "cert_mode":"dns","dns_env":"CF_DNS_API_TOKEN=abc"}}`,
		`{"protocol":"vless","tls":2,"up_mbps":100,"down_mbps":null,
		  "tls_settings":{"server_port":8443,"xver":1,"reject_unknown_sni":true,
		  "cert_mode":"dns","dns_env":{"CF_DNS_API_TOKEN":"abc"}}}`,
	} {
		info, err := parseNodeInfo(c, []byte(body))
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		cm := info.Common
		if cm.UpMbps != 100 || cm.DownMbps != 0 || cm.TlsSettings.ServerPort != "8443" || cm.TlsSettings.Xver != 1 ||
			!cm.CertInfo.RejectUnknownSni || cm.CertInfo.DNSEnv["CF_DNS_API_TOKEN"] != "abc" {
			t.Fatalf("%s: parsed %+v %+v", body, cm.TlsSettings, cm.CertInfo)
		}
	}
	info, err := parseNodeInfo(c, []byte(`{"protocol":"vless","tls_settings":{"reject_unknown_sni":""}}`))
	if err != nil || info.Common.CertInfo.RejectUnknownSni {
		t.Fatalf("empty reject_unknown_sni: %+v %v", info, err)
	}
}

func TestParseNodeInfoTLS(t *testing.T) {
	c := &Client{APIHost: "https://panel.example.com", NodeId: 1}
	for _, tc := range []struct {
		body     string
		security int
		certMode string
		errText  string
	}{
		{body: `{"protocol":"trojan","tls":0}`, security: Tls, certMode: "self"},
		{body: `{"protocol":"anytls","tls":1,"tls_settings":{"cert_mode":""}}`, security: Tls, certMode: "self"},
		{body: `{"protocol":"trojan","tls":2}`, security: Reality},
		{body: `{"protocol":"trojan","tls":1,"tls_settings":{"cert_mode":"none"}}`, security: Tls, certMode: "none"},
		{body: `{"protocol":"hysteria2","tls":0,"tls_settings":{"cert_mode":"none"}}`, security: Tls, certMode: "self"},
		{body: `{"protocol":"tuic","tls":2,"tls_settings":{"cert_mode":"http"}}`, security: Tls, certMode: "http"},
		{body: `{"protocol":"vless","tls":0}`, security: None},
		{body: `{"protocol":"vless","tls":1,"tls_settings":{"cert_mode":"none"}}`, security: Tls, certMode: "none"},
		{body: `{"protocol":"vmess","tls":1,"tls_settings":{"cert_mode":"Self"}}`, security: Tls, certMode: "self"},
		{body: `{"protocol":"vless","tls":1}`, errText: "cert_mode"},
		{body: `{"protocol":"vmess","tls":1,"tls_settings":{"cert_mode":" "}}`, errText: "cert_mode"},
	} {
		info, err := parseNodeInfo(c, []byte(tc.body))
		if tc.errText != "" {
			var configErr *NodeConfigError
			if !errors.As(err, &configErr) || !strings.Contains(err.Error(), tc.errText) {
				t.Fatalf("%s: expected a config error about %s, got %v", tc.body, tc.errText, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if info.Security != tc.security || info.Common.CertInfo.CertMode != tc.certMode {
			t.Fatalf("%s: security %d cert_mode %q, want %d %q", tc.body,
				info.Security, info.Common.CertInfo.CertMode, tc.security, tc.certMode)
		}
	}
}

func TestParseNodeInfoCertDomainFallsBackToHost(t *testing.T) {
	c := &Client{APIHost: "https://panel.example.com", NodeId: 1}
	info, err := parseNodeInfo(c, []byte(`{"protocol":"hysteria2","host":"hy.example.com","tls_settings":{"server_name":""}}`))
	if err != nil || info.Common.CertInfo.CertDomain != "hy.example.com" {
		t.Fatalf("cert domain: %+v %v", info, err)
	}
}

func TestIsEmptyJSON(t *testing.T) {
	for raw, want := range map[string]bool{
		``: true, `null`: true, `{}`: true, ` { } `: true,
		`{"path":"/"}`: false, `[]`: false, `""`: false,
	} {
		if got := IsEmptyJSON(json.RawMessage(raw)); got != want {
			t.Fatalf("IsEmptyJSON(%q) = %v", raw, got)
		}
	}
}
