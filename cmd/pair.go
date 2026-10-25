package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/conf"
)

// Exit codes of tjxt-node pair.
const (
	exitPairFailed      = 1 // other panel answers, e.g. an HTTP 500
	exitPairUsage       = 2 // missing or invalid flags, invalid --api-host URL
	exitPairUnreachable = 3 // the panel cannot be reached
	exitPairBadKey      = 4 // the panel rejected the key
	exitPairGlobalToken = 5 // the key is the fleet-wide server token
	exitPairConfig      = 6 // the config file cannot be written or is kept
	exitPairNotPanel    = 7 // the address does not serve the node API
	exitPairBadNode     = 8 // the panel sent a node config this node cannot use
)

type pairOptions struct {
	APIHost    string
	Key        string
	NodeID     int
	ConfigPath string
	Protocol   string
	Timeout    int
	Force      bool
	Append     bool
	DryRun     bool
}

var pairOpts pairOptions

var pairCommand = cobra.Command{
	Use:   "pair",
	Short: "Pair with a panel node by its key and write the config",
	Long: `Fetch the node config with the node's own key, show which panel node the
key belongs to, and write a config file with that single node.

An existing config keeps its other settings (Log, PprofPort, ...); its Nodes
are replaced by the paired node. --append keeps existing nodes and adds this
one. A config with several nodes is only replaced with --force. --dry-run
checks the key without writing anything.

--api-host and --key default to TJXT_API_HOST and TJXT_API_KEY; prefer the
environment for the key so it does not show up in the process list.

Exit codes: 0 paired, 1 other panel error, 2 invalid flags or URL,
3 panel unreachable, 4 key rejected, 5 global server token instead of a node
key, 6 config file not written.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		o := pairOpts
		if o.APIHost == "" {
			o.APIHost = os.Getenv(conf.EnvAPIHost)
		}
		if o.Key == "" {
			o.Key = os.Getenv(conf.EnvAPIKey)
		}
		return runPair(cmd.Context(), o, cmd.OutOrStdout())
	},
}

func init() {
	f := pairCommand.Flags()
	f.StringVar(&pairOpts.APIHost, "api-host", "", "panel URL, e.g. https://panel.example.com (default $TJXT_API_HOST)")
	f.StringVar(&pairOpts.Key, "key", "", "the node's own key (default $TJXT_API_KEY)")
	f.IntVar(&pairOpts.NodeID, "node-id", 0, "optional: check the key against this node id and send it (legacy keys)")
	f.StringVarP(&pairOpts.ConfigPath, "config", "c", "/etc/tjxt-node/config.json", "config file to write")
	f.StringVar(&pairOpts.Protocol, "protocol", "auto", "panel protocol: auto, v3ui or v1")
	f.IntVar(&pairOpts.Timeout, "timeout", conf.DefaultNodeTimeout, "panel request timeout in seconds")
	f.BoolVar(&pairOpts.Append, "append", false, "append the paired node to an existing config")
	f.BoolVar(&pairOpts.Force, "force", false, "replace a config with several nodes or one that does not parse")
	f.BoolVar(&pairOpts.DryRun, "dry-run", false, "check the key without writing the config")
	command.AddCommand(&pairCommand)
}

func pairFail(code int, format string, args ...any) error {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

// runPair checks the key against the panel and writes the config.
func runPair(ctx context.Context, o pairOptions, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	host, err := validatePanelURL(o.APIHost)
	if err != nil {
		return &exitError{code: exitPairUsage, err: err}
	}
	if strings.TrimSpace(o.Key) == "" {
		return pairFail(exitPairUsage, "--key (or %s) is required", conf.EnvAPIKey)
	}
	if o.Force && o.Append {
		return pairFail(exitPairUsage, "--force and --append cannot be used together")
	}
	switch o.Protocol {
	case "auto", "v3ui", "v1":
	default:
		return pairFail(exitPairUsage, "--protocol must be auto, v3ui or v1, got %q", o.Protocol)
	}
	if o.Timeout <= 0 {
		return pairFail(exitPairUsage, "--timeout must be positive")
	}
	if o.NodeID < 0 {
		return pairFail(exitPairUsage, "--node-id must not be negative")
	}

	retry := 0
	nc := conf.NodeConfig{
		APIHost:    host,
		NodeID:     o.NodeID,
		Key:        strings.TrimSpace(o.Key),
		Timeout:    o.Timeout,
		RetryCount: &retry,
		Protocol:   o.Protocol,
	}
	fmt.Fprintf(out, "Pairing with %s using key %s\n", host, maskKey(nc.Key))
	client, err := panel.New(&nc)
	if err != nil {
		return pairFail(exitPairFailed, "create panel client: %v", err)
	}
	info, err := client.GetNodeInfo(ctx)
	if err != nil {
		return classifyPairError(host, err)
	}
	if info == nil {
		return pairFail(exitPairFailed, "panel %s returned no node config", host)
	}
	name := ""
	if info.Common != nil {
		name = info.Common.Name
	}
	fmt.Fprintf(out, "Key belongs to node %d (type %s", info.Id, info.Type)
	if name != "" {
		fmt.Fprintf(out, ", name %q", name)
	}
	fmt.Fprintln(out, ")")
	if info.Common != nil && info.Common.ServerPort > 0 {
		if info.Common.Host != "" {
			fmt.Fprintf(out, "Node address %s\n", net.JoinHostPort(info.Common.Host, strconv.Itoa(info.Common.ServerPort)))
		} else {
			fmt.Fprintf(out, "Node port %d\n", info.Common.ServerPort)
		}
	}
	nc.NodeID = info.Id
	if o.DryRun {
		fmt.Fprintf(out, "Dry run: %s not written\n", o.ConfigPath)
		return nil
	}
	nc.RetryCount = nil
	if o.Append {
		if _, err := os.Stat(o.ConfigPath); err != nil {
			return pairFail(exitPairConfig, "append requires an existing config %s: %v", o.ConfigPath, err)
		}
		exists, err := conf.MatchesNode(o.ConfigPath, nc)
		if err != nil {
			return pairFail(exitPairConfig, "read existing config: %v", err)
		}
		if exists {
			fmt.Fprintf(out, "Node %d already exists in %s; keeping the existing config\n", info.Id, o.ConfigPath)
			return nil
		}
	}
	if o.Append {
		err = conf.AppendNode(o.ConfigPath, nc)
	} else {
		err = conf.WriteSingleNode(o.ConfigPath, nc, o.Force)
	}
	if err != nil {
		return pairFail(exitPairConfig, "write config: %v", err)
	}
	if o.Append {
		fmt.Fprintf(out, "Appended node %d to %s\n", info.Id, o.ConfigPath)
	} else {
		fmt.Fprintf(out, "Wrote %s with node %d\n", o.ConfigPath, info.Id)
	}
	return nil
}

// validatePanelURL accepts an http(s) URL with a host and returns it without
// a trailing slash.
func validatePanelURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("--api-host (or %s) is required", conf.EnvAPIHost)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid --api-host %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("invalid --api-host %q: want http(s)://host[:port]", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid --api-host %q: no credentials, query or fragment allowed", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

func classifyPairError(host string, err error) error {
	var se *panel.StatusError
	var nce *panel.NodeConfigError
	var netErr net.Error
	var urlErr *url.Error
	switch {
	case panel.IsUnauthorized(err):
		return pairFail(exitPairBadKey, "the panel rejected the key: check it in the panel's node list; it may have been rotated (%v)", err)
	case errors.Is(err, panel.ErrNotPairingKey):
		return &exitError{code: exitPairGlobalToken, err: err}
	case errors.Is(err, panel.ErrNotNodeAPI):
		return pairFail(exitPairNotPanel, "%s does not serve the node API (%v); check --api-host", host, err)
	case errors.As(err, &nce):
		return pairFail(exitPairBadNode, "the panel's config for this node cannot be used: %v; update tjxt-node or fix the node in the panel", err)
	case errors.As(err, &se):
		if se.Code == 404 {
			return pairFail(exitPairNotPanel, "%s does not serve the node API (HTTP 404); check --api-host", host)
		}
		return pairFail(exitPairFailed, "panel error: %v", err)
	case errors.As(err, &netErr), errors.As(err, &urlErr), errors.Is(err, context.DeadlineExceeded):
		return pairFail(exitPairUnreachable, "cannot reach the panel at %s: %v", host, err)
	default:
		return pairFail(exitPairFailed, "pairing failed: %v", err)
	}
}

// maskKey hides a key in output; at most its last four characters remain.
func maskKey(key string) string {
	if len(key) < 12 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}
