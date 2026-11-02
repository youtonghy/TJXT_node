package panel

import (
	"context"
	"net/http"
	"testing"
)

func TestCertificateTrustHeaders(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		_, _ = w.Write([]byte(`{"users":[]}`))
	})
	c := newTestClient(t, p.URL, "v1")
	c.SetCertTrust("first-pin", "first-cert")
	if _, err := c.GetUserList(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.SetCertTrust("second-pin", "second-cert")
	if _, err := c.GetUserList(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.SetCertFingerprint("legacy-pin")
	if _, err := c.GetUserList(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.SetCertTrust("", "")
	if _, err := c.GetUserList(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, want := range [][2]string{{"first-pin", "first-cert"}, {"second-pin", "second-cert"}, {"legacy-pin", ""}, {"", ""}} {
		req := p.Requests()[i]
		if req.Header.Get(CertFingerprintHeader) != want[0] || req.Header.Get(CertDERHeader) != want[1] {
			t.Fatalf("request %d reported stale or mixed certificate trust", i)
		}
	}
}
