package node

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// A self-signed certificate is made with a domain, an IP host or no name at all
// (clients pin it by fingerprint).
func TestSelfSignedCertificateNames(t *testing.T) {
	for _, tc := range []struct{ domain, dns, ip string }{
		{domain: "node.example.com", dns: "node.example.com"},
		{domain: "203.0.113.7", ip: "203.0.113.7"},
		{domain: ""},
	} {
		dir := t.TempDir()
		certFile, keyFile := filepath.Join(dir, "c.cer"), filepath.Join(dir, "c.key")
		if err := generateSelfSslCertificate(tc.domain, certFile, keyFile); err != nil {
			t.Fatalf("%q: %v", tc.domain, err)
		}
		data, err := os.ReadFile(certFile)
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(data)
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("%q: parse: %v", tc.domain, err)
		}
		if (tc.dns == "") != (len(cert.DNSNames) == 0) || (tc.dns != "" && cert.DNSNames[0] != tc.dns) {
			t.Fatalf("%q: DNS names %v", tc.domain, cert.DNSNames)
		}
		if (tc.ip == "") != (len(cert.IPAddresses) == 0) || (tc.ip != "" && cert.IPAddresses[0].String() != tc.ip) {
			t.Fatalf("%q: IP addresses %v", tc.domain, cert.IPAddresses)
		}
	}
}
