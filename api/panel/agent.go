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
