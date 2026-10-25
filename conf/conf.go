package conf

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

// Environment variables that describe a single node, e.g. for Docker. When
// TJXT_API_HOST and TJXT_API_KEY are both set they replace the config file's
// Nodes with one node (TJXT_NODE_ID optional); the file's other settings still
// apply, and the file may then be missing altogether.
//
// TJXT_API_HOST with TJXT_MACHINE_KEY instead sets the Machine (and clears the
// file's Nodes): the agent then runs every node the panel assigns to it.
const (
	EnvAPIHost    = "TJXT_API_HOST"
	EnvAPIKey     = "TJXT_API_KEY"
	EnvNodeID     = "TJXT_NODE_ID"
	EnvMachineKey = "TJXT_MACHINE_KEY"
)

const DefaultNodeRetryCount = 1
const DefaultNodeTimeout = 15

type Conf struct {
	LogConfig   LogConfig    `mapstructure:"Log"`
	NodeConfigs []NodeConfig `mapstructure:"Nodes"`
	// Machine pairs the whole server once: the agent runs every node the panel
	// assigns to the machine and follows the list as it changes. It may sit
	// next to Nodes; a node in both runs once, as the machine's.
	Machine   *MachineConfig `mapstructure:"Machine"`
	PprofPort int            `mapstructure:"PprofPort"`
}

type LogConfig struct {
	Level  string `mapstructure:"Level"`
	Output string `mapstructure:"Output"`
	Access string `mapstructure:"Access"`
}

type NodeConfig struct {
	APIHost string `mapstructure:"ApiHost"`
	// NodeID is optional: 0 pairs by key, the panel finds the node from its
	// own ApiKey. A set NodeID is sent along and checked against the panel.
	NodeID     int    `mapstructure:"NodeID"`
	Key        string `mapstructure:"ApiKey"`
	Timeout    int    `mapstructure:"Timeout"`
	RetryCount *int   `mapstructure:"RetryCount"`
	// Protocol is auto (default), v3ui, or v1.
	Protocol string `mapstructure:"Protocol"`
}

// MachineConfig is the panel connection of a machine (server) pairing.
type MachineConfig struct {
	APIHost string `mapstructure:"ApiHost"`
	// Key is the machine key from the panel's machine list.
	Key        string `mapstructure:"ApiKey"`
	Timeout    int    `mapstructure:"Timeout"`
	RetryCount *int   `mapstructure:"RetryCount"`
	// Protocol is auto (default), v3ui, or v1.
	Protocol string `mapstructure:"Protocol"`
}

// Client returns the connection settings for the machine's own requests (its
// node list): the machine key without a node id.
func (m MachineConfig) Client() NodeConfig {
	return NodeConfig{APIHost: m.APIHost, Key: m.Key, Timeout: m.Timeout, RetryCount: m.RetryCount, Protocol: m.Protocol}
}

// Node returns the settings of a node the machine runs: the machine key with
// the node's id, which the panel accepts for the machine's nodes only.
func (m MachineConfig) Node(id int) NodeConfig {
	n := m.Client()
	n.NodeID = id
	return n
}

// SameMachine reports whether a and b connect the same way.
func SameMachine(a, b *MachineConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	retry := func(p *int) int {
		if p == nil {
			return DefaultNodeRetryCount
		}
		return *p
	}
	return a.APIHost == b.APIHost && a.Key == b.Key && a.Timeout == b.Timeout &&
		a.Protocol == b.Protocol && retry(a.RetryCount) == retry(b.RetryCount)
}

// SameAPIHost compares panel addresses, ignoring trailing slashes.
func SameAPIHost(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

func New() *Conf {
	return &Conf{
		LogConfig: LogConfig{
			Level:  "info",
			Output: "",
			Access: "none",
		},
	}
}

// LoadFromPath reads the config file, applies the environment node (see
// EnvAPIHost) and validates the result.
func (p *Conf) LoadFromPath(filePath string) error {
	envNode, envMachine, err := fromEnv()
	if err != nil {
		return err
	}
	if err := p.loadFile(filePath); err != nil {
		if envNode == nil && envMachine == nil || !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	switch {
	case envNode != nil:
		p.NodeConfigs = []NodeConfig{*envNode}
		p.Machine = nil
	case envMachine != nil:
		p.NodeConfigs = nil
		p.Machine = envMachine
	}
	for i := range p.NodeConfigs {
		if p.NodeConfigs[i].RetryCount == nil {
			p.NodeConfigs[i].RetryCount = intPtr(DefaultNodeRetryCount)
		}
	}
	if p.Machine != nil && p.Machine.RetryCount == nil {
		p.Machine.RetryCount = intPtr(DefaultNodeRetryCount)
	}
	return p.Validate()
}

func (p *Conf) loadFile(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("open config file error: %w", err)
	}
	v := viper.New()
	v.SetConfigFile(filePath)
	// A file without a known extension (such as config.json.pairing, the
	// temporary copy older installers pair) is read as JSON.
	if ext := strings.TrimPrefix(filepath.Ext(filePath), "."); !slices.Contains(viper.SupportedExts, ext) {
		v.SetConfigType("json")
	}
	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("read config file error: %s", err)
	}
	if err := v.Unmarshal(p); err != nil {
		return fmt.Errorf("unmarshal config error: %s", err)
	}
	return nil
}

// fromEnv returns the node or the machine described by the environment, or
// neither when it sets no key.
func fromEnv() (*NodeConfig, *MachineConfig, error) {
	machineKey := strings.TrimSpace(os.Getenv(EnvMachineKey))
	if machineKey == "" {
		n, err := nodeFromEnv()
		return n, nil, err
	}
	host := strings.TrimSpace(os.Getenv(EnvAPIHost))
	switch {
	case strings.TrimSpace(os.Getenv(EnvAPIKey)) != "" || strings.TrimSpace(os.Getenv(EnvNodeID)) != "":
		return nil, nil, fmt.Errorf("set %s or %s (with %s), not both", EnvMachineKey, EnvAPIKey, EnvNodeID)
	case host == "":
		return nil, nil, fmt.Errorf("%s needs %s", EnvMachineKey, EnvAPIHost)
	}
	return nil, &MachineConfig{APIHost: host, Key: machineKey, Timeout: DefaultNodeTimeout, Protocol: "auto"}, nil
}

// nodeFromEnv returns the node described by the environment, or nil when
// TJXT_API_HOST and TJXT_API_KEY are unset.
func nodeFromEnv() (*NodeConfig, error) {
	host := strings.TrimSpace(os.Getenv(EnvAPIHost))
	key := strings.TrimSpace(os.Getenv(EnvAPIKey))
	rawID := strings.TrimSpace(os.Getenv(EnvNodeID))
	if host == "" && key == "" {
		if rawID != "" {
			return nil, fmt.Errorf("%s is set but %s and %s are not", EnvNodeID, EnvAPIHost, EnvAPIKey)
		}
		return nil, nil
	}
	if host == "" || key == "" {
		return nil, fmt.Errorf("set both %s and %s, or neither", EnvAPIHost, EnvAPIKey)
	}
	n := &NodeConfig{APIHost: host, Key: key, Timeout: DefaultNodeTimeout, Protocol: "auto"}
	if rawID != "" {
		id, err := strconv.Atoi(rawID)
		if err != nil || id < 0 {
			return nil, fmt.Errorf("%s must be a non-negative integer, got %q", EnvNodeID, rawID)
		}
		n.NodeID = id
	}
	return n, nil
}

// Validate checks the fields every node (and the machine) needs to reach its
// panel. NodeID is optional (0 pairs by key).
func (p *Conf) Validate() error {
	if len(p.NodeConfigs) == 0 && p.Machine == nil {
		return fmt.Errorf("config has no Nodes and no Machine")
	}
	if m := p.Machine; m != nil {
		switch {
		case m.APIHost == "":
			return fmt.Errorf("machine: ApiHost is empty")
		case m.Key == "":
			return fmt.Errorf("machine: ApiKey is empty")
		}
	}
	for i, n := range p.NodeConfigs {
		switch {
		case n.APIHost == "":
			return fmt.Errorf("node %d: ApiHost is empty", i)
		case n.NodeID < 0:
			return fmt.Errorf("node %d: NodeID must not be negative", i)
		case n.Key == "":
			return fmt.Errorf("node %d: ApiKey is empty", i)
		}
	}
	return nil
}

func intPtr(v int) *int {
	return &v
}
