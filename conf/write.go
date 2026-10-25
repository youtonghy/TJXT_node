package conf

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrMultiNodeConfig means WriteSingleNode would replace several nodes.
var ErrMultiNodeConfig = errors.New("config has several nodes")

// ErrNodeAlreadyConfigured means AppendNode would add a node already present.
var ErrNodeAlreadyConfigured = errors.New("node is already configured")

// MatchesNode reads the file without environment overrides and compares the
// supplied connection settings to its nodes.
func MatchesNode(path string, n NodeConfig) (bool, error) {
	c := New()
	if err := c.loadFile(path); err != nil {
		return false, err
	}
	for _, old := range c.NodeConfigs {
		if strings.TrimRight(old.APIHost, "/") == strings.TrimRight(n.APIHost, "/") && old.Key == n.Key &&
			(old.NodeID == 0 || old.NodeID == n.NodeID) &&
			(old.Protocol == n.Protocol || old.Protocol == "" && n.Protocol == "auto") {
			return true, nil
		}
	}
	return false, nil
}

// AppendNode appends n to an existing JSON config while preserving its other
// settings and existing nodes. Duplicate keys or known node IDs are rejected.
func AppendNode(path string, n NodeConfig) error {
	doc := map[string]json.RawMessage{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read existing config: %w", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		if err == nil {
			err = errors.New("config is null")
		}
		return fmt.Errorf("existing config %s is not valid JSON: %w", path, err)
	}
	var nodes []json.RawMessage
	found := false
	for key, value := range doc {
		if strings.EqualFold(key, "Nodes") {
			if found {
				return errors.New("config has multiple Nodes entries")
			}
			found = true
			if err := json.Unmarshal(value, &nodes); err != nil || nodes == nil {
				return fmt.Errorf("existing config %s has an unreadable Nodes entry", path)
			}
			delete(doc, key)
		}
	}
	if !found {
		return errors.New("config has no Nodes entry")
	}
	for _, value := range nodes {
		var existing nodeFile
		if err := json.Unmarshal(value, &existing); err != nil || existing.APIHost == "" || existing.APIKey == "" {
			return errors.New("config contains an invalid node")
		}
		if strings.TrimRight(existing.APIHost, "/") == strings.TrimRight(n.APIHost, "/") && (existing.APIKey == n.Key || n.NodeID > 0 && existing.NodeID == n.NodeID) {
			return fmt.Errorf("%w (node id %d); replace the existing node instead", ErrNodeAlreadyConfigured, n.NodeID)
		}
	}
	newNode, err := json.Marshal(nodeFile{APIHost: n.APIHost, APIKey: n.Key, NodeID: n.NodeID, Timeout: n.Timeout, Protocol: n.Protocol})
	if err != nil {
		return fmt.Errorf("encode node: %w", err)
	}
	nodes = append(nodes, newNode)
	encoded, err := json.Marshal(nodes)
	if err != nil {
		return fmt.Errorf("encode nodes: %w", err)
	}
	doc["Nodes"] = encoded
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return writeFileAtomic(path, append(out, '\n'))
}

// nodeFile is how WriteSingleNode writes a node.
type nodeFile struct {
	APIHost  string `json:"ApiHost"`
	APIKey   string `json:"ApiKey"`
	NodeID   int    `json:"NodeID,omitempty"`
	Timeout  int    `json:"Timeout,omitempty"`
	Protocol string `json:"Protocol,omitempty"`
}

// WriteSingleNode writes the config file at path with n as its only node. An
// existing JSON config keeps every top-level setting (Log, PprofPort, ...) and
// only its Nodes array is replaced; one with more than one node is refused
// unless force is set (ErrMultiNodeConfig), and with force an unreadable one is
// replaced. A missing Log section gets the defaults. The file is written to a
// temporary file in the same directory and renamed over path, with mode 0600;
// a missing directory is created with mode 0750.
func WriteSingleNode(path string, n NodeConfig, force bool) error {
	doc := map[string]json.RawMessage{}
	old, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("read existing config: %w", err)
	default:
		if err := json.Unmarshal(old, &doc); err != nil {
			if !force {
				return fmt.Errorf("existing config %s is not valid JSON (%v); fix it, or use --force to replace it", path, err)
			}
			doc = map[string]json.RawMessage{}
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
	}
	hasLog := false
	for k, v := range doc {
		switch {
		case strings.EqualFold(k, "Log"):
			hasLog = true
		case strings.EqualFold(k, "Nodes"):
			var nodes []json.RawMessage
			if err := json.Unmarshal(v, &nodes); err != nil && !force {
				return fmt.Errorf("existing config %s has an unreadable Nodes entry (%v); use --force to replace it", path, err)
			}
			if len(nodes) > 1 && !force {
				return fmt.Errorf("%w (%d) in %s; pairing replaces Nodes with one node, use --force to do that", ErrMultiNodeConfig, len(nodes), path)
			}
			delete(doc, k)
		}
	}
	if !hasLog {
		def := New().LogConfig
		raw, err := json.Marshal(map[string]string{"Level": def.Level, "Output": def.Output, "Access": def.Access})
		if err != nil {
			return err
		}
		doc["Log"] = raw
	}
	nodes, err := json.Marshal([]nodeFile{{
		APIHost:  n.APIHost,
		APIKey:   n.Key,
		NodeID:   n.NodeID,
		Timeout:  n.Timeout,
		Protocol: n.Protocol,
	}})
	if err != nil {
		return err
	}
	doc["Nodes"] = nodes
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return writeFileAtomic(path, append(out, '\n'))
}

// writeFileAtomic replaces path with data (mode 0600) so readers, including
// the config watcher, see either the old or the complete new file. An existing
// file's owner is kept when possible, so a running service can still read it.
func writeFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod temporary config: %w", err)
	}
	if fi, statErr := os.Stat(path); statErr == nil {
		keepOwner(f, fi)
	}
	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
