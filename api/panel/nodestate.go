package panel

import (
	"regexp"
	"strings"

	"github.com/go-resty/resty/v2"
)

// NodeStateHeader carries the node's runtime state on config pulls: "ok" when
// its last apply succeeded, "down" when its inbound failed to come up. It is
// omitted until the node has tried to apply once.
const NodeStateHeader = "X-TJXT-Node-State"

// NodeErrorHeader carries a short reason with a "down" state.
const NodeErrorHeader = "X-TJXT-Node-Error"

const (
	nodeStateOK   = "ok"
	nodeStateDown = "down"
)

// maxNodeErrorLen bounds the reason sent in NodeErrorHeader, in bytes.
const maxNodeErrorLen = 200

// SetNodeUp reports that the node's last apply succeeded.
func (c *Client) SetNodeUp() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodeState = nodeStateOK
	c.nodeError = ""
}

// SetNodeDown reports that the node's inbound is down because of reason. The
// reason is sanitized for a header: secrets (the node key, the given values,
// URL credentials and long key-like strings) are redacted, control characters
// removed and the result truncated.
func (c *Client) SetNodeDown(reason string, secrets ...string) {
	reason = sanitizeNodeError(reason, append(secrets, c.Token)...)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodeState = nodeStateDown
	c.nodeError = reason
}

// setNodeStateHeaders adds the node state headers to a config pull.
func (c *Client) setNodeStateHeaders(req *resty.Request) {
	c.mu.Lock()
	state, reason := c.nodeState, c.nodeError
	c.mu.Unlock()
	if state == "" {
		return
	}
	req.SetHeader(NodeStateHeader, state)
	if state == nodeStateDown && reason != "" {
		req.SetHeader(NodeErrorHeader, reason)
	}
}

const redacted = "[redacted]"

// minRedactedLen is the shortest secret value replaced in a reason.
const minRedactedLen = 4

var (
	// urlUserinfo matches the credentials of a URL ("scheme://user:pass@").
	urlUserinfo = regexp.MustCompile(`://[^/@\s]+@`)
	// keyLike matches long base64, hex or UUID-like runs: keys, tokens, seeds.
	keyLike = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)
)

// sanitizeNodeError makes reason a single printable ASCII line of at most
// maxNodeErrorLen bytes with secrets redacted.
func sanitizeNodeError(reason string, secrets ...string) string {
	for _, secret := range secrets {
		// Very short values would redact ordinary words; the key-like pattern
		// below covers real keys anyway.
		if len(secret) >= minRedactedLen {
			reason = strings.ReplaceAll(reason, secret, redacted)
		}
	}
	reason = urlUserinfo.ReplaceAllString(reason, "://"+redacted+"@")
	reason = keyLike.ReplaceAllString(reason, redacted)
	var b strings.Builder
	space := false
	for _, r := range reason {
		if r < 0x20 || r == 0x7f || r > 0x7e {
			// Line breaks, tabs and other control or non-ASCII characters
			// become one space.
			r = ' '
		}
		if r == ' ' {
			if space {
				continue
			}
			space = true
		} else {
			space = false
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if len(out) > maxNodeErrorLen {
		out = strings.TrimSpace(out[:maxNodeErrorLen])
	}
	return out
}

// Secrets returns the credentials in the node's config (keys, passwords, DNS
// provider credentials), for redacting them from reported errors.
func (n *NodeInfo) Secrets() []string {
	if n == nil || n.Common == nil {
		return nil
	}
	cm := n.Common
	secrets := []string{
		cm.TlsSettings.PrivateKey,
		cm.TlsSettings.Mldsa65Seed,
		cm.EncryptionSettings.PrivateKey,
		cm.ServerKey,
		cm.ObfsPassword,
	}
	for _, v := range cm.TlsSettings.DNSEnv {
		secrets = append(secrets, v)
	}
	if cm.CertInfo != nil {
		for _, v := range cm.CertInfo.DNSEnv {
			secrets = append(secrets, v)
		}
	}
	return secrets
}
