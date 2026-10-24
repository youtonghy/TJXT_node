package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/infra/conf"
	coreConf "github.com/xtls/xray-core/infra/conf"
	"github.com/youtonghy/TJXT_node/api/panel"
)

type NetworkSettingsProxyProtocol struct {
	AcceptProxyProtocol bool `json:"acceptProxyProtocol"`
}

func (v *V2Core) removeInbound(tag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return v.ihm.RemoveHandler(ctx, tag)
}

func (v *V2Core) addInbound(config *core.InboundHandlerConfig) error {
	rawHandler, err := core.CreateObject(v.Server, config)
	if err != nil {
		return err
	}
	handler, ok := rawHandler.(inbound.Handler)
	if !ok {
		return fmt.Errorf("not an InboundHandler: %s", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := v.ihm.AddHandler(ctx, handler); err != nil {
		return err
	}
	return nil
}

// BuildInbound build Inbound config for different protocol
func buildInbound(nodeInfo *panel.NodeInfo, tag string, users []panel.UserInfo) (*core.InboundHandlerConfig, error) {
	in := &coreConf.InboundDetourConfig{}
	var err error
	switch nodeInfo.Type {
	case "vless":
		err = buildVLess(nodeInfo, in)
	case "vmess":
		err = buildVMess(nodeInfo, in)
	case "trojan":
		err = buildTrojan(nodeInfo, in)
	case "shadowsocks":
		err = buildShadowsocks(nodeInfo, in)
	case "hysteria2":
		err = buildHysteria2(nodeInfo, in)
	case "tuic":
		err = buildTuic(nodeInfo, in)
	case "anytls":
		err = buildAnyTLS(nodeInfo, in)
	case "http":
		err = buildHTTP(nodeInfo, in, users)
	case "mixed":
		err = buildMixed(nodeInfo, in, users)
	case "tunnel":
		err = buildTunnel(nodeInfo, in)
	case "tun":
		err = buildTun(nodeInfo, in)
	case "wireguard":
		err = buildWireGuard(nodeInfo, in, tag, users)
	default:
		return nil, fmt.Errorf("unsupported node type: %s", nodeInfo.Type)
	}
	if err != nil {
		return nil, err
	}
	if err := applyPanelStreamSettings(nodeInfo, in); err != nil {
		return nil, err
	}
	// Set network protocol
	if !panel.IsEmptyJSON(nodeInfo.Common.NetworkSettings) {
		n := &NetworkSettingsProxyProtocol{}
		err := json.Unmarshal(nodeInfo.Common.NetworkSettings, n)
		if err != nil {
			return nil, fmt.Errorf("unmarshal network settings error: %s", err)
		}
		if n.AcceptProxyProtocol {
			if in.StreamSetting == nil {
				t := coreConf.TransportProtocol(nodeInfo.Common.Network)
				in.StreamSetting = &coreConf.StreamConfig{
					Network: &t,
					SocketSettings: &coreConf.SocketConfig{
						AcceptProxyProtocol: n.AcceptProxyProtocol,
					},
				}
			} else {
				in.StreamSetting.SocketSettings = &coreConf.SocketConfig{
					AcceptProxyProtocol: n.AcceptProxyProtocol,
				}
			}
		}
	}
	// Set socket settings for trusted X-Forwarded-For headers
	if len(nodeInfo.Common.TrustedXForwardedFor) > 0 {
		if in.StreamSetting == nil {
			return nil, errors.New("stream settings must be configured to set trusted X-Forwarded-For headers")
		}
		if in.StreamSetting.SocketSettings == nil {
			in.StreamSetting.SocketSettings = &coreConf.SocketConfig{}
		}
		in.StreamSetting.SocketSettings.TrustedXForwardedFor = nodeInfo.Common.TrustedXForwardedFor
	}

	if nodeInfo.Type != "tun" {
		in.PortList = &coreConf.PortList{
			Range: []coreConf.PortRange{
				{
					From: uint32(nodeInfo.Common.ServerPort),
					To:   uint32(nodeInfo.Common.ServerPort),
				}},
		}
		listenIP := nodeInfo.Common.ListenIP
		if listenIP == "" {
			listenIP = "0.0.0.0"
		}
		ipAddress := net.ParseAddress(listenIP)
		in.ListenOn = &coreConf.Address{Address: ipAddress}
	}
	// Set SniffingConfig
	sniffingConfig := &coreConf.SniffingConfig{
		Enabled:      true,
		DestOverride: coreConf.StringList{"http", "tls", "quic"},
	}
	in.SniffingConfig = sniffingConfig
	applyPanelSniffing(nodeInfo, in)

	// Set TLS or Reality settings
	switch nodeInfo.Security {
	case panel.Tls:
		if nodeInfo.Common.CertInfo == nil {
			return nil, errors.New("the CertInfo is not vail")
		}
		switch nodeInfo.Common.CertInfo.CertMode {
		case "none", "":
			// TLS is terminated in front of the node, which serves plaintext.
			// parseNodeInfo gives TLS-only protocols a certificate mode and
			// refuses an empty one for vless and vmess.
		default:
			if in.StreamSetting == nil {
				in.StreamSetting = &coreConf.StreamConfig{}
			}
			in.StreamSetting.Security = "tls"
			in.StreamSetting.TLSSettings = &coreConf.TLSConfig{
				Certs: []*coreConf.TLSCertConfig{
					{
						CertFile:     nodeInfo.Common.CertInfo.CertFile,
						KeyFile:      nodeInfo.Common.CertInfo.KeyFile,
						OcspStapling: 3600,
					},
				},
				RejectUnknownSNI: nodeInfo.Common.CertInfo.RejectUnknownSni,
			}
			if nodeInfo.Type == "hysteria2" || nodeInfo.Type == "tuic" {
				alpnList := &coreConf.StringList{"h3"}
				in.StreamSetting.TLSSettings.ALPN = alpnList
			}
		}
	case panel.Reality:
		if in.StreamSetting == nil {
			in.StreamSetting = &coreConf.StreamConfig{}
		}
		in.StreamSetting.Security = "reality"
		v := nodeInfo.Common
		serverNames := v.TlsSettings.EffectiveServerNames()
		shortIds := v.TlsSettings.EffectiveShortIds()
		dest := v.TlsSettings.Dest
		if dest == "" {
			dest = v.TlsSettings.PrimaryServerName()
		}
		xver := v.TlsSettings.Xver
		if xver < 0 {
			return nil, fmt.Errorf("invalid reality xver %d", xver)
		}
		port := string(v.TlsSettings.ServerPort)
		if port == "" {
			port = "443"
		}
		d, err := json.Marshal(net.JoinHostPort(dest, port))
		if err != nil {
			return nil, fmt.Errorf("marshal reality dest error: %s", err)
		}
		in.StreamSetting.REALITYSettings = &coreConf.REALITYConfig{
			Dest:         d,
			Xver:         uint64(xver),
			Show:         false,
			ServerNames:  serverNames,
			PrivateKey:   v.TlsSettings.PrivateKey,
			MinClientVer: "0.0.1",
			ShortIds:     shortIds,
			Mldsa65Seed:  v.TlsSettings.Mldsa65Seed,
		}
	default:
		break
	}
	in.Tag = tag
	return in.Build()
}

func buildVLess(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig) error {
	v := nodeInfo.Common
	inbound.Protocol = "vless"
	var err error
	decryption := "none"
	if enc := strings.ToLower(strings.TrimSpace(nodeInfo.Common.Encryption)); enc != "" && enc != "none" {
		switch enc {
		case "mlkem768x25519plus":
			encSettings := nodeInfo.Common.EncryptionSettings
			parts := []string{
				"mlkem768x25519plus",
				encSettings.Mode,
				encSettings.Ticket,
			}
			if encSettings.ServerPadding != "" {
				parts = append(parts, encSettings.ServerPadding)
			}
			parts = append(parts, encSettings.PrivateKey)
			decryption = strings.Join(parts, ".")
		default:
			return fmt.Errorf("vless decryption method %s is not support", nodeInfo.Common.Encryption)
		}
	}
	s, err := json.Marshal(&coreConf.VLessInboundConfig{
		Decryption: decryption,
	})
	if err != nil {
		return fmt.Errorf("marshal vless config error: %s", err)
	}
	inbound.Settings = (*json.RawMessage)(&s)
	return applyTransport(v, inbound)
}

func buildVMess(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig) error {
	v := nodeInfo.Common
	// Set vmess
	inbound.Protocol = "vmess"
	var err error
	s, err := json.Marshal(&coreConf.VMessInboundConfig{})
	if err != nil {
		return fmt.Errorf("marshal vmess settings error: %s", err)
	}
	inbound.Settings = (*json.RawMessage)(&s)
	return applyTransport(v, inbound)
}

func buildTrojan(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig) error {
	inbound.Protocol = "trojan"
	v := nodeInfo.Common
	s, err := json.Marshal(&coreConf.TrojanServerConfig{})
	if err != nil {
		return fmt.Errorf("marshal trojan settings error: %s", err)
	}
	inbound.Settings = (*json.RawMessage)(&s)
	return applyTransport(v, inbound)
}

// transportNetworks are the panel transports applyTransport supports. Xray
// removed the HTTP/2 ("http", "h2") and QUIC transports.
var transportNetworks = []string{"tcp", "raw", "ws", "grpc", "httpupgrade", "xhttp", "splithttp"}

// applyTransport sets the panel's transport (network and network_settings) on
// a stream-based inbound: vless, vmess, trojan and anytls. An empty network is
// tcp; missing, null or {} settings leave the transport's defaults.
func applyTransport(v *panel.CommonNode, inbound *coreConf.InboundDetourConfig) error {
	network := strings.ToLower(strings.TrimSpace(v.Network))
	if network == "" {
		network = "tcp"
	}
	t := coreConf.TransportProtocol(network)
	inbound.StreamSetting = &coreConf.StreamConfig{Network: &t}
	var target any
	switch network {
	case "tcp", "raw":
		target = &inbound.StreamSetting.TCPSettings
	case "ws":
		target = &inbound.StreamSetting.WSSettings
	case "grpc":
		target = &inbound.StreamSetting.GRPCSettings
	case "httpupgrade":
		target = &inbound.StreamSetting.HTTPUPGRADESettings
	case "splithttp", "xhttp":
		target = &inbound.StreamSetting.SplitHTTPSettings
	default:
		return fmt.Errorf("unsupported network %q for %s; use one of: %s",
			v.Network, v.Protocol, strings.Join(transportNetworks, ", "))
	}
	if panel.IsEmptyJSON(v.NetworkSettings) {
		return nil
	}
	if err := json.Unmarshal(v.NetworkSettings, target); err != nil {
		return fmt.Errorf("unmarshal %s settings error: %s", network, err)
	}
	return nil
}

type ShadowsocksHTTPNetworkSettings struct {
	AcceptProxyProtocol bool   `json:"acceptProxyProtocol"`
	Path                string `json:"path"`
	Host                string `json:"Host"`
}

func buildShadowsocks(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig) error {
	inbound.Protocol = "shadowsocks"
	s := nodeInfo.Common
	method, err := ssMethod(s.Cipher, s.ServerKey)
	if err != nil {
		return err
	}
	settings := &coreConf.ShadowsocksServerConfig{
		Cipher: method.name,
	}
	p := make([]byte, 32)
	if _, err := rand.Read(p); err != nil {
		return fmt.Errorf("generate random password error: %s", err)
	}
	randomPasswd := hex.EncodeToString(p)
	cipher := method.name
	if method.is2022 {
		settings.Password = s.ServerKey
		// Shadowsocks 2022 user keys must have the cipher's key length.
		randomPasswd = base64.StdEncoding.EncodeToString(p[:method.keyLen])
		cipher = ""
	}
	defaultSSuser := &coreConf.ShadowsocksUserConfig{
		Cipher:   cipher,
		Password: randomPasswd,
	}
	settings.Users = append(settings.Users, defaultSSuser)
	// Default: support both tcp and udp
	settings.NetworkList = &coreConf.NetworkList{"tcp", "udp"}
	// Only set StreamSetting when NetworkSettings is configured
	if !panel.IsEmptyJSON(s.NetworkSettings) {
		shttp := &ShadowsocksHTTPNetworkSettings{}
		err := json.Unmarshal(s.NetworkSettings, shttp)
		if err != nil {
			return fmt.Errorf("unmarshal shadowsocks settings error: %s", err)
		}
		// HTTP obfuscation requires TCP only (PROXY protocol can work with UDP)
		if shttp.Path != "" || shttp.Host != "" {
			// Restrict protocol-level network list to TCP only for HTTP obfuscation
			settings.NetworkList = &coreConf.NetworkList{"tcp"}
		}

		// Set StreamSetting for TCP features (PROXY protocol and/or HTTP obfuscation)
		if shttp.AcceptProxyProtocol || shttp.Path != "" || shttp.Host != "" {
			t := coreConf.TransportProtocol("tcp")
			inbound.StreamSetting = &coreConf.StreamConfig{Network: &t}
			inbound.StreamSetting.TCPSettings = &coreConf.TCPConfig{}
			inbound.StreamSetting.TCPSettings.AcceptProxyProtocol = shttp.AcceptProxyProtocol
			// Set HTTP header settings if path or host is configured
			if shttp.Path != "" || shttp.Host != "" {
				httpHeader := map[string]interface{}{
					"type":    "http",
					"request": map[string]interface{}{},
				}
				request := httpHeader["request"].(map[string]interface{})
				// Use "/" as default path if not specified
				path := shttp.Path
				if path == "" {
					path = "/"
				}
				request["path"] = []string{path}
				if shttp.Host != "" {
					request["headers"] = map[string]interface{}{
						"Host": []string{shttp.Host},
					}
				}
				headerJSON, err := json.Marshal(httpHeader)
				if err == nil {
					inbound.StreamSetting.TCPSettings.HeaderConfig = json.RawMessage(headerJSON)
				}
			}
		}
	}

	sets, err := json.Marshal(settings)
	inbound.Settings = (*json.RawMessage)(&sets)
	if err != nil {
		return fmt.Errorf("marshal shadowsocks settings error: %s", err)
	}
	return nil
}

func buildHysteria2(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig) error {
	inbound.Protocol = "hysteria"
	s := nodeInfo.Common
	settings := &coreConf.HysteriaServerConfig{
		Version: 2,
	}

	t := coreConf.TransportProtocol("hysteria")
	up := conf.Bandwidth(strconv.Itoa(int(s.UpMbps)) + "mbps")
	down := conf.Bandwidth(strconv.Itoa(int(s.DownMbps)) + "mbps")
	inbound.StreamSetting = &coreConf.StreamConfig{Network: &t}
	hysteriasetting := &coreConf.HysteriaConfig{
		Version: 2,
	}
	finalmask := &coreConf.FinalMask{}
	if !s.Ignore_Client_Bandwidth && (s.UpMbps > 0 || s.DownMbps > 0) {
		finalmask.QuicParams = &coreConf.QuicParamsConfig{
			Congestion: "force-brutal",
			BrutalUp:   up,
			BrutalDown: down,
		}
	}
	if s.Obfs != "" && s.ObfsPassword != "" {
		obfs, err := json.Marshal(map[string]string{"password": s.ObfsPassword})
		if err != nil {
			return fmt.Errorf("marshal hysteria2 obfs settings error: %s", err)
		}
		rawobfsJSON := json.RawMessage(obfs)
		finalmask.Udp = []conf.Mask{
			{
				Type:     s.Obfs,
				Settings: &rawobfsJSON,
			},
		}
	}
	inbound.StreamSetting.FinalMask = finalmask
	sets, err := json.Marshal(settings)
	inbound.Settings = (*json.RawMessage)(&sets)
	inbound.StreamSetting.HysteriaSettings = hysteriasetting
	if err != nil {
		return fmt.Errorf("marshal hysteria2 settings error: %s", err)
	}
	return nil
}

func buildTuic(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig) error {
	inbound.Protocol = "tuic"
	s := nodeInfo.Common
	settings := &coreConf.TuicServerConfig{
		CongestionControl: s.CongestionControl,
		ZeroRttHandshake:  bool(s.ZeroRTTHandshake),
	}
	t := coreConf.TransportProtocol("tuic")
	inbound.StreamSetting = &coreConf.StreamConfig{Network: &t}
	sets, err := json.Marshal(settings)
	inbound.Settings = (*json.RawMessage)(&sets)
	if err != nil {
		return fmt.Errorf("marshal tuic settings error: %s", err)
	}
	return nil
}

func buildAnyTLS(nodeInfo *panel.NodeInfo, inbound *coreConf.InboundDetourConfig) error {
	inbound.Protocol = "anytls"
	v := nodeInfo.Common
	settings := &coreConf.AnyTLSServerConfig{
		PaddingScheme: v.PaddingScheme,
	}
	if err := applyTransport(v, inbound); err != nil {
		return err
	}
	sets, err := json.Marshal(settings)
	inbound.Settings = (*json.RawMessage)(&sets)
	if err != nil {
		return fmt.Errorf("marshal anytls settings error: %s", err)
	}
	return nil
}
