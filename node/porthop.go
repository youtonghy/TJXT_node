package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/youtonghy/TJXT_node/api/panel"
)

// Hysteria 2 port hopping: clients send to ports across hop_ports and the node
// redirects them to its listen port with an nftables table of its own (Linux
// only; the service has CAP_NET_ADMIN). Without nftables clients still reach
// the listen port itself.

// portHopSupported is whether this OS has nftables; tests set it.
var portHopSupported = runtime.GOOS == "linux"

// runNft runs an nft script; tests replace it.
var runNft = func(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nft: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// hopTable names the nftables table of one node.
func hopTable(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return "tjxt_hop_" + hex.EncodeToString(sum[:6])
}

// hopPortSet turns "20000-30000, 443" into an nftables set "20000-30000, 443";
// anything that is not a port or a range is refused.
func hopPortSet(ports string) (string, error) {
	var parts []string
	for _, part := range strings.Split(ports, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bounds := strings.SplitN(part, "-", 2)
		for _, bound := range bounds {
			if n, err := strconv.Atoi(strings.TrimSpace(bound)); err != nil || n < 1 || n > 65535 {
				return "", fmt.Errorf("invalid hop port %q", part)
			}
		}
		parts = append(parts, strings.ReplaceAll(part, " ", ""))
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("no hop ports")
	}
	return strings.Join(parts, ", "), nil
}

// hopsPorts reports whether info asks for port hopping.
func hopsPorts(info *panel.NodeInfo) bool {
	return info != nil && info.Common != nil && info.Type == "hysteria2" &&
		strings.TrimSpace(info.Common.HopPorts) != ""
}

// applyPortHop installs (or replaces) the node's redirect of its hop ports.
func applyPortHop(tag string, info *panel.NodeInfo) error {
	if !portHopSupported {
		return fmt.Errorf("port hopping needs nftables (Linux)")
	}
	set, err := hopPortSet(info.Common.HopPorts)
	if err != nil {
		return err
	}
	table := hopTable(tag)
	// "table" then "delete table" makes the replacement idempotent.
	script := fmt.Sprintf("table inet %[1]s\ndelete table inet %[1]s\ntable inet %[1]s {\n"+
		"\tchain prerouting {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n"+
		"\t\tudp dport { %[2]s } redirect to :%[3]d\n\t}\n}\n", table, set, info.Common.ServerPort)
	return runNft(script)
}

// removePortHop removes the node's redirect, if any.
func removePortHop(tag string) error {
	if !portHopSupported {
		return nil
	}
	table := hopTable(tag)
	return runNft(fmt.Sprintf("table inet %[1]s\ndelete table inet %[1]s\n", table))
}

// syncPortHop matches the redirect to whether the inbound runs with hop ports.
func (c *Controller) syncPortHop(running bool) {
	info := c.nodeInfo()
	if running && hopsPorts(info) {
		if err := applyPortHop(c.tag, info); err != nil {
			log.WithFields(log.Fields{"tag": c.tag, "err": err}).
				Warn("Port hopping is not set up; clients still reach the listen port")
		}
		c.portHopApplied = true
		return
	}
	if c.portHopApplied {
		if err := removePortHop(c.tag); err != nil {
			log.WithFields(log.Fields{"tag": c.tag, "err": err}).Warn("Removing the port hopping redirect failed")
		}
		c.portHopApplied = false
	}
}
