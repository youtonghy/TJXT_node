package core

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/proxy/shadowsocks_2022"
	"github.com/youtonghy/TJXT_node/api/panel"
)

// Public test material shared with the Rust panel's ss2022_tests.rs.
const ss2022TestSeed = `"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="`
const ss2022TestUUID = "11111111-2222-3333-4444-555555555555"

func TestSS2022CrossLanguageVectors(t *testing.T) {
	for _, tc := range []struct{ cipher, expected string }{
		{"2022-blake3-aes-128-gcm", "h00t27VbIWbTGuCSrEqW5A=="},
		{"2022-blake3-aes-256-gcm", "iSAGGeQF1Fl5ikuRMz3YmacP3yXClTgHqVPME3LlH/I="},
	} {
		users, err := buildSSUsers("test", []panel.UserInfo{{Id: 1, Uuid: ss2022TestUUID}}, " "+strings.ToUpper(tc.cipher)+" ", "test-server-key", json.RawMessage(ss2022TestSeed))
		if err != nil {
			t.Fatal(err)
		}
		account, err := users[0].Account.GetInstance()
		if err != nil {
			t.Fatal(err)
		}
		if got := account.(*shadowsocks_2022.Account).Key; got != tc.expected {
			t.Fatalf("%s: wrong KDF output", tc.cipher)
		}
		method, _ := ssMethod(tc.cipher, "test-server-key")
		method.userKeySeed, _ = ss2022Seed(json.RawMessage(ss2022TestSeed))
		other, _ := ss2022UserPSK(method, ss2022TestUUID+"x")
		if other == tc.expected {
			t.Fatal("KDF ignored credential suffix")
		}
		method.userKeySeed = nil
		legacy, err := ss2022UserPSK(method, ss2022TestUUID)
		if err != nil || legacy != base64.StdEncoding.EncodeToString([]byte(ss2022TestUUID[:method.keyLen])) {
			t.Fatal("legacy derivation changed")
		}
	}
}

func TestSS2022MalformedSeedNeverDowngrades(t *testing.T) {
	for _, bad := range []string{`null`, `7`, `""`, `"bad"`, `"AAAAAAAAAAAAAAAAAAAAAA=="`, `"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=\n"`} {
		_, err := buildSSUsers("test", []panel.UserInfo{{Id: 1, Uuid: ss2022TestUUID}}, "2022-blake3-aes-128-gcm", "test-server-key", json.RawMessage(bad))
		if err == nil || !strings.Contains(err.Error(), "ss2022_user_key_seed") {
			t.Fatal("bad seed accepted by user builder")
		}
		raw := `{"protocol":"shadowsocks","cipher":"2022-blake3-aes-128-gcm","server_key":"L8dsfbeHPFvIGfC9zOUbKA==","ss2022_user_key_seed":` + bad + `}`
		if _, err := buildInbound(testNode(t, raw), "test", nil); err == nil || !strings.Contains(err.Error(), "ss2022_user_key_seed") {
			t.Fatal("bad seed accepted by inbound builder")
		}
	}
	if _, err := buildSSUsers("test", []panel.UserInfo{{Id: 1, Uuid: "short"}}, "2022-blake3-aes-128-gcm", "test-server-key", nil); err == nil {
		t.Fatal("short legacy UUID accepted")
	}
	if _, err := buildSSUsers("test", []panel.UserInfo{{Id: 1, Uuid: "short"}}, "2022-blake3-aes-128-gcm", "test-server-key", json.RawMessage(ss2022TestSeed)); err != nil {
		t.Fatal(err)
	}
	if seed, err := ss2022Seed(nil); err != nil || seed != nil {
		t.Fatal("missing seed must stay legacy")
	}
}
