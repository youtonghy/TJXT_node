package crypt

import "testing"

func TestWireGuardKeysAreDeterministic(t *testing.T) {
	uuid := "11111111-1111-1111-1111-111111111111"
	priv := WireGuardClientPrivate(uuid)
	if len(priv) != 32 {
		t.Fatalf("private key length %d", len(priv))
	}
	if KeyB64(priv) != KeyB64(WireGuardClientPrivate(uuid)) {
		t.Fatal("client private key must be stable")
	}
	if KeyB64(WireGuardClientPublic(uuid)) == KeyB64(priv) {
		t.Fatal("public key must differ from private key")
	}
	serverA := WireGuardServerPrivate("edge.example.com", 51820, "")
	serverB := WireGuardServerPrivate("edge.example.com", 51820, "")
	if KeyB64(serverA) != KeyB64(serverB) {
		t.Fatal("server key from host:port must be stable")
	}
}

func TestMTProtoSecretIncludesDomain(t *testing.T) {
	plain := MTProtoSecret("uuid-1", "")
	if len(plain) != 34 || plain[:2] != "dd" {
		t.Fatalf("plain secret %s", plain)
	}
	fakeTLS := MTProtoSecret("uuid-1", "t.me")
	if fakeTLS[:2] != "ee" {
		t.Fatalf("faketls secret %s", fakeTLS)
	}
}
