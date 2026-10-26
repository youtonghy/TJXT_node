package core

import (
	"fmt"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/routing"
	coreConf "github.com/xtls/xray-core/infra/conf"
	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/conf"
	"github.com/youtonghy/TJXT_node/core/app/dispatcher"
	_ "github.com/youtonghy/TJXT_node/core/distro/all"
	"google.golang.org/protobuf/proto"
)

type AddUsersParams struct {
	// Tag labels the users (their email) and their traffic and limits.
	Tag string
	// Inbound is the inbound the users join; empty means the inbound tagged Tag.
	Inbound string
	Users   []panel.UserInfo
	*panel.NodeInfo
}

type V2Core struct {
	Config   *conf.Conf
	ReloadCh chan struct{}
	access   sync.Mutex
	Server   *core.Instance
	users    *UserMap
	// shared holds the inbounds whose users belong to several nodes, by tag.
	sharedMu   sync.Mutex
	shared     map[string]*sharedInbound
	ihm        inbound.Manager
	ohm        outbound.Manager
	dispatcher *dispatcher.DefaultDispatcher
}

type UserMap struct {
	uidMap  map[string]int
	mapLock sync.RWMutex
}

func New(config *conf.Conf) *V2Core {
	core := &V2Core{
		Config: config,
		users: &UserMap{
			uidMap: make(map[string]int),
		},
	}
	return core
}

func (v *V2Core) Start(infos []*panel.NodeInfo) error {
	v.access.Lock()
	defer v.access.Unlock()
	server, err := getCore(v.Config, infos)
	if err != nil {
		return err
	}
	if err := server.Start(); err != nil {
		if cerr := server.Close(); cerr != nil {
			log.WithField("err", cerr).Error("Close core after failed start failed")
		}
		return fmt.Errorf("start xray core: %w", err)
	}
	v.Server = server
	v.ihm = v.Server.GetFeature(inbound.ManagerType()).(inbound.Manager)
	v.ohm = v.Server.GetFeature(outbound.ManagerType()).(outbound.Manager)
	v.dispatcher = v.Server.GetFeature(routing.DispatcherType()).(*dispatcher.DefaultDispatcher)
	return nil
}

func (v *V2Core) Close() error {
	v.access.Lock()
	defer v.access.Unlock()
	v.Config = nil
	v.sharedMu.Lock()
	v.shared = nil
	v.sharedMu.Unlock()
	v.ihm = nil
	v.ohm = nil
	v.dispatcher = nil
	if v.Server == nil {
		return nil
	}
	err := v.Server.Close()
	v.Server = nil
	if err != nil {
		return err
	}
	return nil
}

// BuildConfig builds the Xray configuration for the given nodes without
// starting anything, so a new configuration can be validated while the
// current core keeps running.
func BuildConfig(c *conf.Conf, infos []*panel.NodeInfo) (*core.Config, error) {
	// Log Config
	coreLogConfig := &coreConf.LogConfig{
		LogLevel:  c.LogConfig.Level,
		AccessLog: c.LogConfig.Access,
		ErrorLog:  c.LogConfig.Output,
	}
	// Custom config
	dnsConfig, outBoundConfig, routeConfig, err := GetCustomConfig(infos)
	if err != nil {
		return nil, fmt.Errorf("build custom config: %w", err)
	}
	// Inbound config
	var inBoundConfig []*core.InboundHandlerConfig

	// Policy config
	levelPolicyConfig := &coreConf.Policy{
		StatsUserUplink:   true,
		StatsUserDownlink: true,
		Handshake:         proto.Uint32(4),
		ConnectionIdle:    proto.Uint32(120),
		UplinkOnly:        proto.Uint32(2),
		DownlinkOnly:      proto.Uint32(4),
		BufferSize:        proto.Int32(128),
	}
	corePolicyConfig := &coreConf.PolicyConfig{}
	corePolicyConfig.Levels = map[uint32]*coreConf.Policy{0: levelPolicyConfig}
	policyConfig, err := corePolicyConfig.Build()
	if err != nil {
		return nil, fmt.Errorf("build policy config: %w", err)
	}
	// Build Xray conf
	return &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(coreLogConfig.Build()),
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&stats.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
			serial.ToTypedMessage(policyConfig),
			serial.ToTypedMessage(dnsConfig),
			serial.ToTypedMessage(routeConfig),
		},
		Inbound:  inBoundConfig,
		Outbound: outBoundConfig,
	}, nil
}

// SameConfig reports whether nodes a and b build the same Xray config (DNS,
// outbounds and routing; inbounds are added per node at runtime), so a core
// started for a can serve b by adding and removing inbounds only.
func SameConfig(c *conf.Conf, a, b []*panel.NodeInfo) (bool, error) {
	ca, err := BuildConfig(c, a)
	if err != nil {
		return false, err
	}
	cb, err := BuildConfig(c, b)
	if err != nil {
		return false, err
	}
	return sameCoreConfig(ca, cb), nil
}

// sameCoreConfig compares two Xray configs by the messages their typed
// settings decode to: the encoded bytes may differ for equal messages (map
// fields are marshalled in random order). A config that cannot be decoded
// counts as different, which only costs a full reload.
func sameCoreConfig(a, b *core.Config) bool {
	if len(a.App) != len(b.App) || len(a.Inbound) != len(b.Inbound) || len(a.Outbound) != len(b.Outbound) {
		return false
	}
	for i := range a.App {
		if !sameTyped(a.App[i], b.App[i]) {
			return false
		}
	}
	for i := range a.Inbound {
		x, y := a.Inbound[i], b.Inbound[i]
		if x.Tag != y.Tag || !sameTyped(x.ReceiverSettings, y.ReceiverSettings) || !sameTyped(x.ProxySettings, y.ProxySettings) {
			return false
		}
	}
	for i := range a.Outbound {
		x, y := a.Outbound[i], b.Outbound[i]
		if x.Tag != y.Tag || !sameTyped(x.SenderSettings, y.SenderSettings) || !sameTyped(x.ProxySettings, y.ProxySettings) {
			return false
		}
	}
	return true
}

func sameTyped(a, b *serial.TypedMessage) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Type != b.Type {
		return false
	}
	ma, err := a.GetInstance()
	if err != nil {
		return false
	}
	mb, err := b.GetInstance()
	if err != nil {
		return false
	}
	return proto.Equal(ma, mb)
}

func getCore(c *conf.Conf, infos []*panel.NodeInfo) (*core.Instance, error) {
	config, err := BuildConfig(c, infos)
	if err != nil {
		return nil, err
	}
	server, err := core.New(config)
	if err != nil {
		return nil, fmt.Errorf("create xray instance: %w", err)
	}
	log.Info("Xray Core Version: ", core.Version())
	return server, nil
}
