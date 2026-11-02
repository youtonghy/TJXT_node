package node

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicCertificateTrust(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "node.cer"), filepath.Join(dir, "node.key")
	if err := generateSelfSslCertificate("node.example.com", certFile, keyFile); err != nil {
		t.Fatal(err)
	}
	pin, encoded, err := certTrust(certFile)
	if err != nil {
		t.Fatal(err)
	}
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	if pin != hex.EncodeToString(sum[:]) {
		t.Fatal("certificate and pin disagree")
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "node.example.com"}); err != nil {
		t.Fatalf("reported certificate cannot verify its server: %v", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "wrong.example.com"}); err == nil {
		t.Fatal("certificate accepted a different server name")
	}
	if _, _, err := certTrust(keyFile); err == nil {
		t.Fatal("reported a private key as a certificate")
	}
	bad := filepath.Join(dir, "bad.cer")
	if err := os.WriteFile(bad, []byte("-----BEGIN CERTIFICATE-----\nYmFk\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := certTrust(bad); err == nil {
		t.Fatal("reported invalid certificate DER")
	}
}
