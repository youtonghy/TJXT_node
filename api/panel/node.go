package panel

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	// InboundTag is the Xray inbound the node's users join: its own Tag, or for a
	// node that shares another node's inbound (SharedInbound) that node's tag.
	InboundTag string
	// SharedInbound is the id of the node whose inbound this node shares, 0 if none.
	SharedInbound int
	Common        *CommonNode
}

// Inbound returns the tag of the Xray inbound the node's users join.
func (n *NodeInfo) Inbound() string {
	if n.InboundTag != "" {
		return n.InboundTag
	}
	return n.Tag
}

// Shared reports whether the node is a line of another node's inbound.
func (n *NodeInfo) Shared() bool {
	return n.SharedInbound > 0 && n.Inbound() != n.Tag
}

type CommonNode struct {
	Protocol   string      `json:"protocol"`
	Name       string      `json:"name"`
	Host       string      `json:"host"`
	ListenIP   string      `json:"listen_ip"`
	ServerPort int         `json:"server_port"`
	Routes     []Route     `json:"routes"`
	BaseConfig *BaseConfig `json:"base_config"`
	// SharedInbound is the node whose inbound (port, protocol, transport,
	// TLS/REALITY) this node shares; its users get their own credentials there.
	SharedInbound int `json:"shared_inbound"`
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
	CongestionControl string   `json:"congestion_control"`
	ZeroRTTHandshake  FlexBool `json:"zero_rtt_handshake"`
	//anytls
	PaddingScheme []string `json:"padding_scheme,omitempty"`
	//hysteria hysteria2
	UpMbps   FlexInt `json:"up_mbps"`
	DownMbps FlexInt `json:"down_mbps"`
	Obfs     string  `json:"obfs"`
	// HopPorts are the Hysteria 2 port hopping ranges ("20000-30000,40000-41000");
	// the node redirects them to ServerPort.
	HopPorts                string   `json:"hop_ports"`
	ObfsPassword            string   `json:"obfs_password"`
	Ignore_Client_Bandwidth FlexBool `json:"ignore_client_bandwidth"`
}

type Route struct {
	Id          int             `json:"id"`
	Rule        json.RawMessage `json:"rule,omitempty"`
	Match       []string        `json:"match,omitempty"`
	Action      string          `json:"action,omitempty"`
	ActionValue *string         `json:"action_value,omitempty"`
}

type BaseConfig struct {
	PushInterval           any `json:"push_interval"`
	PullInterval           any `json:"pull_interval"`
	DeviceOnlineMinTraffic int `json:"device_online_min_traffic"`
	// NodeID is the node the panel resolved from the key. The panel adds it to
	// every config it serves for one node, whichever key was used; a config
	// without it means the panel could not tell which node this is (see
	// ErrNotPairingKey). The top-level "id" is not trusted.
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
	ServerName  string   `json:"server_name"`
	ServerNames []string `json:"server_names"`
	Dest        string   `json:"dest"`
	// ServerPort is the REALITY target port, sent as a string or a number.
	ServerPort  FlexString `json:"server_port"`
	ShortId     string     `json:"short_id"`
	ShortIds    []string   `json:"short_ids"`
	PrivateKey  string     `json:"private_key"`
	Mldsa65Seed string     `json:"mldsa65Seed"`
	Xver        FlexInt    `json:"xver"`
	CertMode    string     `json:"cert_mode"`
	CertFile    string     `json:"cert_file"`
	KeyFile     string     `json:"key_file"`
	Provider    string     `json:"provider"`
	// DNSEnv is "KEY=value,KEY2=value2" or a JSON object.
	DNSEnv           EnvMap   `json:"dns_env"`
	RejectUnknownSni FlexBool `json:"reject_unknown_sni"`
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

// ErrNotNodeAPI means the address answered with something that is not JSON,
// e.g. a web page: it is not the panel's node API.
var ErrNotNodeAPI = errors.New("the reply is not JSON; the address is not the panel's node API")

// NodeConfigError means the panel sent a node config this node cannot use,
// e.g. a field of the wrong type or an unsupported protocol.
type NodeConfigError struct{ Err error }

func (e *NodeConfigError) Error() string { return e.Err.Error() }
func (e *NodeConfigError) Unwrap() error { return e.Err }

func parseNodeInfo(c *Client, body []byte) (*NodeInfo, error) {
	if !json.Valid(body) {
		return nil, ErrNotNodeAPI
	}
	cm := &CommonNode{}
	if err := json.Unmarshal(body, cm); err != nil {
		return nil, &NodeConfigError{fmt.Errorf("decode node params error: %s", err)}
	}
	id, err := c.resolveNodeID(cm.BaseConfig)
	if err != nil {
		return nil, err
	}
	node := &NodeInfo{Id: id}
	if IsEmptyJSON(cm.NetworkSettings) && !IsEmptyJSON(cm.StreamSettings) {
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
		return nil, &NodeConfigError{errors.New("mtproto nodes are retired; delete this node in the panel")}
	default:
		return nil, &NodeConfigError{fmt.Errorf("unsupport protocol: %s", cm.Protocol)}
	}
	certMode, err := resolveTLS(node, cm.TlsSettings.CertMode)
	if err != nil {
		return nil, &NodeConfigError{err}
	}
	node.Tag = fmt.Sprintf("[%s]-%s:%d", c.APIHost, node.Type, node.Id)
	node.InboundTag = node.Tag
	// The inbound and its certificate belong to the node that owns them.
	inboundID := node.Id
	if cm.SharedInbound > 0 && cm.SharedInbound != node.Id {
		node.SharedInbound = cm.SharedInbound
		inboundID = cm.SharedInbound
		node.InboundTag = fmt.Sprintf("[%s]-%s:%d", c.APIHost, node.Type, inboundID)
	}
	cf := cm.TlsSettings.CertFile
	kf := cm.TlsSettings.KeyFile
	if cf == "" {
		cf = filepath.Join("/etc/tjxt-node/", cm.Protocol+strconv.Itoa(inboundID)+".cer")
	}
	if kf == "" {
		kf = filepath.Join("/etc/tjxt-node/", cm.Protocol+strconv.Itoa(inboundID)+".key")
	}
	certDomain := cm.TlsSettings.PrimaryServerName()
	if certDomain == "" {
		certDomain = cm.Host
	}
	cm.CertInfo = &CertInfo{
		CertMode:         certMode,
		CertFile:         cf,
		KeyFile:          kf,
		Email:            "node@tjxt",
		CertDomain:       certDomain,
		DNSEnv:           make(map[string]string),
		Provider:         cm.TlsSettings.Provider,
		RejectUnknownSni: bool(cm.TlsSettings.RejectUnknownSni),
	}
	if certMode == "dns" {
		for k, v := range cm.TlsSettings.DNSEnv {
			cm.CertInfo.DNSEnv[k] = v
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

// tlsOnlyProtocol reports whether a protocol is always served over TLS.
func tlsOnlyProtocol(nodeType string) bool {
	switch nodeType {
	case "trojan", "anytls", "hysteria2", "tuic":
		return true
	default:
		return false
	}
}

// quicProtocol reports whether a protocol runs over QUIC, which has TLS built in:
// neither REALITY nor TLS terminated in front of the node can serve it.
func quicProtocol(nodeType string) bool {
	return nodeType == "hysteria2" || nodeType == "tuic"
}

// resolveTLS fixes node.Security for protocols that only exist over TLS and
// returns the certificate mode to use.
//
//   - trojan and anytls without TLS ("tls": 0) are served with TLS; REALITY
//     ("tls": 2) is kept. hysteria2 and tuic always use TLS.
//   - With TLS on, a TLS-only protocol without cert_mode gets a self-signed
//     certificate ("self"); hysteria2 and tuic do so for "none" as well, since
//     QUIC cannot be terminated in front of the node.
//   - vless and vmess with TLS on need a cert_mode: an empty one is an error
//     rather than silent plaintext. "none" stays allowed and serves plaintext,
//     for TLS terminated in front of the node (a CDN or reverse proxy).
func resolveTLS(node *NodeInfo, certMode string) (string, error) {
	certMode = strings.ToLower(strings.TrimSpace(certMode))
	if tlsOnlyProtocol(node.Type) && (node.Security == None || quicProtocol(node.Type)) {
		node.Security = Tls
	}
	if node.Security != Tls {
		return certMode, nil
	}
	switch {
	case tlsOnlyProtocol(node.Type) && certMode == "":
		return "self", nil
	case quicProtocol(node.Type) && certMode == "none":
		return "self", nil
	case (node.Type == "vless" || node.Type == "vmess") && certMode == "":
		return "", fmt.Errorf("%s has TLS on but no tls_settings.cert_mode; set self, file, http or dns, "+
			"or none when TLS is terminated in front of the node", node.Type)
	}
	return certMode, nil
}

// IsEmptyJSON reports whether a raw panel value is missing, null or {}.
func IsEmptyJSON(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return true
	}
	if s[0] != '{' {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && len(object) == 0
}

// FlexInt is a panel number. Panels may send numbers as JSON numbers or numeric
// strings; "" and null are 0.
type FlexInt int

func (n *FlexInt) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "null" {
		return nil
	}
	if unquoted, err := strconv.Unquote(s); err == nil {
		s = strings.TrimSpace(unquoted)
	}
	if s == "" {
		*n = 0
		return nil
	}
	if v, err := strconv.Atoi(s); err == nil {
		*n = FlexInt(v)
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) > math.MaxInt32 {
		return fmt.Errorf("invalid number %s", data)
	}
	*n = FlexInt(f)
	return nil
}

// FlexString is a panel value sent as a string or a number; null is "".
type FlexString string

func (s *FlexString) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "null" {
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		*s = FlexString(strings.TrimSpace(str))
		return nil
	}
	if _, err := strconv.ParseFloat(raw, 64); err != nil {
		return fmt.Errorf("invalid string or number %s", data)
	}
	*s = FlexString(raw)
	return nil
}

// EnvMap is a set of environment variables, sent as "KEY=value,KEY2=value2" or
// as a JSON object of strings, numbers or booleans; null and "" are empty.
type EnvMap map[string]string

func (m *EnvMap) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "null" {
		return nil
	}
	env := EnvMap{}
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		for _, pair := range strings.Split(str, ",") {
			if strings.TrimSpace(pair) == "" {
				continue
			}
			k, v, ok := strings.Cut(pair, "=")
			k = strings.TrimSpace(k)
			if !ok || k == "" {
				return fmt.Errorf("invalid environment entry %q, want KEY=value", pair)
			}
			env[k] = strings.TrimSpace(v)
		}
		*m = env
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return fmt.Errorf("invalid environment %s: want \"KEY=value,...\" or an object", data)
	}
	for k, v := range object {
		switch v := v.(type) {
		case string:
			env[k] = v
		case float64, bool:
			env[k] = fmt.Sprint(v)
		case nil:
			env[k] = ""
		default:
			return fmt.Errorf("invalid environment value for %s: want a string", k)
		}
	}
	*m = env
	return nil
}

// FlexBool is a panel switch. Panels store switches as 0/1, so it accepts
// JSON booleans, numbers (non-zero is true), "0"/"1"/"true"/"false", "" and
// null (both false).
type FlexBool bool

func (b *FlexBool) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "null" {
		return nil
	}
	if unquoted, err := strconv.Unquote(s); err == nil {
		s = strings.TrimSpace(unquoted)
	}
	if s == "" {
		*b = false
		return nil
	}
	if v, err := strconv.ParseBool(s); err == nil {
		*b = FlexBool(v)
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("invalid switch value %s", data)
	}
	*b = f != 0
	return nil
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
