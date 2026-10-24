package panel

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"encoding/json"
)

// Security type
const (
	None    = 0
	Tls     = 1
	Reality = 2
)

type NodeInfo struct {
	Id           int
	Type         string
	Security     int
	PushInterval time.Duration
	PullInterval time.Duration
	Tag          string
	Common       *CommonNode
}

type CommonNode struct {
	Protocol   string      `json:"protocol"`
	Name       string      `json:"name"`
	Host       string      `json:"host"`
	ListenIP   string      `json:"listen_ip"`
	ServerPort int         `json:"server_port"`
	Routes     []Route     `json:"routes"`
	BaseConfig *BaseConfig `json:"base_config"`
	//vless vmess trojan
	Tls                  int         `json:"tls"`
	TlsSettings          TlsSettings `json:"tls_settings"`
	CertInfo             *CertInfo
	Network              string          `json:"network"`
	NetworkSettings      json.RawMessage `json:"network_settings"`
	StreamSettings       json.RawMessage `json:"streamSettings"`
	Settings             json.RawMessage `json:"settings"`
	Sniffing             json.RawMessage `json:"sniffing"`
	TrustedXForwardedFor []string        `json:"trusted_x_forwarded_for"`
	Encryption           string          `json:"encryption"`
	EncryptionSettings   EncSettings     `json:"encryption_settings"`
	ServerName           string          `json:"server_name"`
	Flow                 string          `json:"flow"`
	//shadowsocks
	Cipher    string `json:"cipher"`
	ServerKey string `json:"server_key"`
	//tuic
	CongestionControl string `json:"congestion_control"`
	ZeroRTTHandshake  bool   `json:"zero_rtt_handshake"`
	//anytls
	PaddingScheme []string `json:"padding_scheme,omitempty"`
	//hysteria hysteria2
	UpMbps   int    `json:"up_mbps"`
	DownMbps int    `json:"down_mbps"`
	Obfs     string `json:"obfs"`
	// HopPorts are the Hysteria 2 port hopping ranges ("20000-30000,40000-41000");
	// the node redirects them to ServerPort.
	HopPorts                string `json:"hop_ports"`
	ObfsPassword            string `json:"obfs_password"`
	Ignore_Client_Bandwidth bool   `json:"ignore_client_bandwidth"`
}

type Route struct {
	Id          int      `json:"id"`
	Match       []string `json:"match"`
	Action      string   `json:"action"`
	ActionValue *string  `json:"action_value"`
}

type BaseConfig struct {
	PushInterval           any `json:"push_interval"`
	PullInterval           any `json:"pull_interval"`
	DeviceOnlineMinTraffic int `json:"device_online_min_traffic"`
	// NodeID is sent only to a node authenticated with its own key; the
	// fleet-wide token never gets it. The top-level "id" is not trusted.
	NodeID               int `json:"node_id"`
	NodeReportMinTraffic int `json:"node_report_min_traffic"`
	// AllowPrivateEgress lets users reach private, loopback, link-local and
	// metadata addresses through this node. Absent means false.
	AllowPrivateEgress bool `json:"allow_private_egress"`
}

// AllowsPrivateEgress reports whether the panel enabled private egress.
func (n *NodeInfo) AllowsPrivateEgress() bool {
	return n != nil && n.Common != nil && n.Common.BaseConfig != nil && n.Common.BaseConfig.AllowPrivateEgress
}

type TlsSettings struct {
	ServerName       string   `json:"server_name"`
	ServerNames      []string `json:"server_names"`
	Dest             string   `json:"dest"`
	ServerPort       string   `json:"server_port"`
	ShortId          string   `json:"short_id"`
	ShortIds         []string `json:"short_ids"`
	PrivateKey       string   `json:"private_key"`
	Mldsa65Seed      string   `json:"mldsa65Seed"`
	Xver             uint64   `json:"xver,string"`
	CertMode         string   `json:"cert_mode"`
	CertFile         string   `json:"cert_file"`
	KeyFile          string   `json:"key_file"`
	Provider         string   `json:"provider"`
	DNSEnv           string   `json:"dns_env"`
	RejectUnknownSni string   `json:"reject_unknown_sni"`
}

type CertInfo struct {
	CertMode         string
	CertFile         string
	KeyFile          string
	Email            string
	CertDomain       string
	DNSEnv           map[string]string
	Provider         string
	RejectUnknownSni bool
}

type EncSettings struct {
	Mode          string `json:"mode"`
	Ticket        string `json:"ticket"`
	ServerPadding string `json:"server_padding"`
	PrivateKey    string `json:"private_key"`
}

func (c *Client) GetNodeInfo(ctx context.Context) (node *NodeInfo, err error) {
	if c.v3Available() {
		r, data, err := c.callV3(ctx, "GET", "server/UniProxy/config", identityParams(c), c.getNodeEtag())
		if err == nil {
			if r.StatusCode() == 304 {
				return nil, nil
			}
			return c.acceptNodeConfig(r.Header().Get("ETag"), data)
		}
		if !errors.Is(err, errV3Unsupported) {
			return nil, err
		}
	}
	if !c.useV1() {
		return nil, fmt.Errorf("v3 config failed and v1 fallback is disabled")
	}
	const path = "/api/v2/server/config"
	r, err := c.client.
		R().
		SetContext(ctx).
		SetHeader("If-None-Match", c.getNodeEtag()).
		ForceContentType("application/json").
		Get(path)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("received nil response")
	}

	if r.StatusCode() == 304 {
		return nil, nil
	}
	if err := checkResponse(r, nil, "get node config"); err != nil {
		return nil, err
	}
	return c.acceptNodeConfig(r.Header().Get("ETag"), r.Body())
}

// acceptNodeConfig parses a fetched node config; it returns nil when the config
// is unchanged since the last accepted one.
func (c *Client) acceptNodeConfig(etag string, body []byte) (*NodeInfo, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("panel returned an empty node config")
	}
	if !c.nodeConfigChanged(body) {
		return nil, nil
	}
	info, err := parseNodeInfo(c, body)
	if err != nil {
		return nil, err
	}
	c.commitNodeCache(etag, body)
	return info, nil
}

// ErrNotPairingKey means a node without NodeID got a config without
// base_config.node_id: the key is the fleet-wide server token, which cannot
// tell the panel which node this is.
var ErrNotPairingKey = errors.New("this key is not a node pairing key (use the node's own key, not the global server token)")

// resolveNodeID checks the panel's base_config.node_id against the configured
// NodeID and returns the node's id.
func (c *Client) resolveNodeID(bc *BaseConfig) (int, error) {
	reported := 0
	if bc != nil {
		reported = bc.NodeID
	}
	switch {
	case c.NodeId > 0 && reported > 0 && reported != c.NodeId:
		return 0, fmt.Errorf("panel says this key belongs to node %d but NodeID is %d; fix NodeID or ApiKey", reported, c.NodeId)
	case c.NodeId > 0:
		return c.NodeId, nil
	case reported <= 0:
		return 0, ErrNotPairingKey
	}
	c.mu.Lock()
	c.learnedID = reported
	c.mu.Unlock()
	return reported, nil
}

func parseNodeInfo(c *Client, body []byte) (*NodeInfo, error) {
	cm := &CommonNode{}
	if err := json.Unmarshal(body, cm); err != nil {
		return nil, fmt.Errorf("decode node params error: %s", err)
	}
	id, err := c.resolveNodeID(cm.BaseConfig)
	if err != nil {
		return nil, err
	}
	node := &NodeInfo{Id: id}
	if len(cm.NetworkSettings) == 0 && len(cm.StreamSettings) > 0 {
		cm.NetworkSettings = cm.StreamSettings
	}
	switch cm.Protocol {
	case "vmess", "trojan", "hysteria2", "hysteria", "tuic", "anytls", "vless",
		"wireguard", "http", "mixed", "tunnel", "tun":
		node.Type = cm.Protocol
		if node.Type == "hysteria" {
			node.Type = "hysteria2"
		}
		node.Security = cm.Tls
	case "shadowsocks":
		node.Type = cm.Protocol
		node.Security = 0
	case "mtproto":
		return nil, fmt.Errorf("mtproto nodes are retired; delete this node in the panel")
	default:
		return nil, fmt.Errorf("unsupport protocol: %s", cm.Protocol)
	}
	node.Tag = fmt.Sprintf("[%s]-%s:%d", c.APIHost, node.Type, node.Id)
	cf := cm.TlsSettings.CertFile
	kf := cm.TlsSettings.KeyFile
	if cf == "" {
		cf = filepath.Join("/etc/tjxt-node/", cm.Protocol+strconv.Itoa(node.Id)+".cer")
	}
	if kf == "" {
		kf = filepath.Join("/etc/tjxt-node/", cm.Protocol+strconv.Itoa(node.Id)+".key")
	}
	cm.CertInfo = &CertInfo{
		CertMode:         cm.TlsSettings.CertMode,
		CertFile:         cf,
		KeyFile:          kf,
		Email:            "node@tjxt",
		CertDomain:       cm.TlsSettings.PrimaryServerName(),
		DNSEnv:           make(map[string]string),
		Provider:         cm.TlsSettings.Provider,
		RejectUnknownSni: cm.TlsSettings.RejectUnknownSni == "1",
	}
	if cm.CertInfo.CertMode == "dns" && cm.TlsSettings.DNSEnv != "" {
		envs := strings.Split(cm.TlsSettings.DNSEnv, ",")
		for _, env := range envs {
			kv := strings.SplitN(env, "=", 2)
			if len(kv) == 2 {
				cm.CertInfo.DNSEnv[kv[0]] = kv[1]
			}
		}
	}
	if cm.BaseConfig == nil {
		cm.BaseConfig = &BaseConfig{PushInterval: 60, PullInterval: 60}
	}
	node.PushInterval = intervalToTime(cm.BaseConfig.PushInterval)
	node.PullInterval = intervalToTime(cm.BaseConfig.PullInterval)
	node.Common = cm
	return node, nil
}

const defaultInterval = 60 * time.Second

// intervalToTime converts a panel interval in seconds; missing, invalid or
// non-positive values fall back to 60 seconds.
func intervalToTime(i interface{}) time.Duration {
	var d time.Duration
	switch v := i.(type) {
	case nil:
		return defaultInterval
	case int:
		d = time.Duration(v) * time.Second
	case int64:
		d = time.Duration(v) * time.Second
	case float64:
		d = time.Duration(v * float64(time.Second))
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return defaultInterval
		}
		d = time.Duration(n) * time.Second
	default:
		return defaultInterval
	}
	if d <= 0 {
		return defaultInterval
	}
	return d
}

func (t TlsSettings) EffectiveServerNames() []string {
	if len(t.ServerNames) > 0 {
		return t.ServerNames
	}
	if t.ServerName == "" {
		return nil
	}
	return []string{t.ServerName}
}

func (t TlsSettings) EffectiveShortIds() []string {
	if len(t.ShortIds) > 0 {
		return t.ShortIds
	}
	if t.ShortId == "" {
		return nil
	}
	return []string{t.ShortId}
}

func (t TlsSettings) PrimaryServerName() string {
	serverNames := t.EffectiveServerNames()
	if len(serverNames) == 0 {
		return ""
	}
	return serverNames[0]
}
