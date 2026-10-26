package panel

import (
	"fmt"
	"strings"

	xcore "github.com/xtls/xray-core/core"
)

// AgentVersion is this build's version; the command sets it at startup.
var AgentVersion = "dev"

// AgentFeatures are the capabilities of this build the panel may rely on, sent
// with every request so the panel can tell which nodes support what.
var AgentFeatures = []string{
	"vless-encryption",
	"xhttp",
	"hysteria2",
	"tuic",
	"anytls",
	"wireguard",
	"finalmask",
	"revoked-users",
	"device-activity",
	"traffic-spool",
	// Nodes may share another node's inbound (shared_inbound): one listener,
	// per-node credentials, traffic and routes.
	"shared-inbound",
}

// FeaturesHeader carries AgentFeatures, comma separated.
const FeaturesHeader = "X-TJXT-Features"

// userAgent identifies the agent and its Xray core to the panel.
func userAgent() string {
	return fmt.Sprintf("tjxt-node/%s xray/%s", AgentVersion, xcore.Version())
}

func featuresHeader() string {
	return strings.Join(AgentFeatures, ",")
}

// CertFingerprintHeader carries the hex SHA-256 of the node's TLS certificate (DER),
// the value Xray clients pin with pinnedPeerCertSha256.
const CertFingerprintHeader = "X-TJXT-Cert-SHA256"

// SetCertFingerprint reports the node's certificate fingerprint on later requests;
// an empty one stops reporting it.
func (c *Client) SetCertFingerprint(fingerprint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.certSHA256 = fingerprint
}

func (c *Client) certFingerprint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.certSHA256
}
