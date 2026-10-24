package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/anytls"
	hyaccount "github.com/xtls/xray-core/proxy/hysteria/account"
	"github.com/xtls/xray-core/proxy/shadowsocks"
	"github.com/xtls/xray-core/proxy/shadowsocks_2022"
	"github.com/xtls/xray-core/proxy/trojan"
	"github.com/xtls/xray-core/proxy/tuic"
	"github.com/xtls/xray-core/proxy/vless"
	"github.com/youtonghy/TJXT_node/api/panel"
	"github.com/youtonghy/TJXT_node/common/counter"
	"github.com/youtonghy/TJXT_node/common/format"
	"github.com/youtonghy/TJXT_node/core/app/dispatcher"
)

func (v *V2Core) GetUserManager(tag string) (proxy.UserManager, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	handler, err := v.ihm.GetHandler(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("no such inbound tag: %s", err)
	}
	inboundInstance, ok := handler.(proxy.GetInbound)
	if !ok {
		return nil, fmt.Errorf("handler %s is not implement proxy.GetInbound", tag)
	}
	userManager, ok := inboundInstance.GetInbound().(proxy.UserManager)
	if !ok {
		return nil, fmt.Errorf("handler %s is not implement proxy.UserManager", tag)
	}
	return userManager, nil
}

func (vc *V2Core) DelUsers(users []panel.UserInfo, tag string, info *panel.NodeInfo) error {
	vc.users.mapLock.Lock()
	defer vc.users.mapLock.Unlock()
	if info != nil && staticAccountProtocol(info.Type) {
		for i := range users {
			user := format.UserTag(tag, users[i].Uuid)
			delete(vc.users.uidMap, user)
			delete(vc.users.uidMap, users[i].Uuid)
			if v, ok := vc.dispatcher.Counter.Load(tag); ok {
				tc := v.(*counter.TrafficCounter)
				tc.Delete(user)
				tc.Delete(users[i].Uuid)
			}
		}
		return nil
	}
	userManager, err := vc.GetUserManager(tag)
	if err != nil {
		return fmt.Errorf("get user manager error: %s", err)
	}
	var user string
	for i := range users {
		user = format.UserTag(tag, users[i].Uuid)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = userManager.RemoveUser(ctx, user)
		cancel()
		if err != nil {
			return err
		}
		delete(vc.users.uidMap, user)
		delete(vc.users.uidMap, users[i].Uuid)
		if v, ok := vc.dispatcher.Counter.Load(tag); ok {
			tc := v.(*counter.TrafficCounter)
			tc.Delete(user)
			tc.Delete(users[i].Uuid)
		}
		if v, ok := vc.dispatcher.LinkManagers.Load(user); ok {
			lm := v.(*dispatcher.LinkManager)
			lm.CloseAll()
			vc.dispatcher.LinkManagers.Delete(user)
		}
	}
	return nil
}

func (v *V2Core) AddUsers(p *AddUsersParams) (added int, err error) {
	v.users.mapLock.Lock()
	defer v.users.mapLock.Unlock()
	for i := range p.Users {
		v.users.uidMap[format.UserTag(p.Tag, p.Users[i].Uuid)] = p.Users[i].Id
		v.users.uidMap[p.Users[i].Uuid] = p.Users[i].Id
	}
	if staticAccountProtocol(p.NodeInfo.Type) {
		return len(p.Users), nil
	}
	var users []*protocol.User
	switch p.NodeInfo.Type {
	case "vmess":
		users = buildVmessUsers(p.Tag, p.Users)
	case "vless":
		users = buildVlessUsers(p.Tag, p.Users, p.Common.Flow)
	case "trojan":
		users = buildTrojanUsers(p.Tag, p.Users)
	case "shadowsocks":
		users, err = buildSSUsers(p.Tag,
			p.Users,
			p.Common.Cipher,
			p.Common.ServerKey)
		if err != nil {
			return 0, err
		}
	case "hysteria2":
		users = buildHysteria2Users(p.Tag, p.Users)
	case "tuic":
		users = buildTuicUsers(p.Tag, p.Users)
	case "anytls":
		users = buildAnyTLSUsers(p.Tag, p.Users)
	default:
		return 0, fmt.Errorf("unsupported node type: %s", p.NodeInfo.Type)
	}
	man, err := v.GetUserManager(p.Tag)
	if err != nil {
		return 0, fmt.Errorf("get user manager error: %s", err)
	}
	for _, u := range users {
		mUser, err := u.ToMemoryUser()
		if err != nil {
			return 0, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = man.AddUser(ctx, mUser)
		cancel()
		if err != nil {
			return 0, err
		}
	}
	return len(users), nil
}

func buildVmessUsers(tag string, userInfo []panel.UserInfo) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i, user := range userInfo {
		users[i] = buildVmessUser(tag, &user)
	}
	return users
}

func buildVmessUser(tag string, userInfo *panel.UserInfo) (user *protocol.User) {
	vmessAccount := &conf.VMessAccount{
		ID:       userInfo.Uuid,
		Security: "auto",
	}
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(vmessAccount.Build()),
	}
}

func buildVlessUsers(tag string, userInfo []panel.UserInfo, flow string) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		users[i] = buildVlessUser(tag, &(userInfo)[i], flow)
	}
	return users
}

func buildVlessUser(tag string, userInfo *panel.UserInfo, flow string) (user *protocol.User) {
	vlessAccount := &vless.Account{
		Id: userInfo.Uuid,
	}
	vlessAccount.Flow = flow
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(vlessAccount),
	}
}

func buildTrojanUsers(tag string, userInfo []panel.UserInfo) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		users[i] = buildTrojanUser(tag, &(userInfo)[i])
	}
	return users
}

func buildTrojanUser(tag string, userInfo *panel.UserInfo) (user *protocol.User) {
	trojanAccount := &trojan.Account{
		Password: userInfo.Uuid,
	}
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(trojanAccount),
	}
}

func buildSSUsers(tag string, userInfo []panel.UserInfo, cypher string, serverKey string) ([]*protocol.User, error) {
	method, err := ssMethod(cypher, serverKey)
	if err != nil {
		return nil, err
	}
	users := make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		if users[i], err = buildSSUser(tag, &userInfo[i], method); err != nil {
			return nil, err
		}
	}
	return users, nil
}

func buildSSUser(tag string, userInfo *panel.UserInfo, method ssCipher) (*protocol.User, error) {
	if !method.is2022 {
		ssAccount := &shadowsocks.Account{
			Password:   userInfo.Uuid,
			CipherType: method.legacy,
		}
		return &protocol.User{
			Level:   0,
			Email:   format.UserTag(tag, userInfo.Uuid),
			Account: serial.ToTypedMessage(ssAccount),
		}, nil
	}
	if len(userInfo.Uuid) < method.keyLen {
		return nil, fmt.Errorf("user %d: uuid is shorter than the %d-byte %s key", userInfo.Id, method.keyLen, method.name)
	}
	ssAccount := &shadowsocks_2022.Account{
		Key: base64.StdEncoding.EncodeToString([]byte(userInfo.Uuid[:method.keyLen])),
	}
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(ssAccount),
	}, nil
}

// SupportedShadowsocksCiphers are the Shadowsocks methods a node serves. The
// 2022 methods need the panel's server_key. 2022-blake3-chacha20-poly1305 is
// missing: Xray serves it to a single user only, and a node serves many.
var SupportedShadowsocksCiphers = []string{
	"aes-128-gcm",
	"aes-256-gcm",
	"chacha20-poly1305", "chacha20-ietf-poly1305",
	"xchacha20-poly1305", "xchacha20-ietf-poly1305",
	"2022-blake3-aes-128-gcm",
	"2022-blake3-aes-256-gcm",
}

// ssCipher is a Shadowsocks method the node serves.
type ssCipher struct {
	name   string // lower-case method name
	is2022 bool
	keyLen int                    // 2022: user and server key length
	legacy shadowsocks.CipherType // classic AEAD methods
}

// ssMethod resolves the panel's cipher. Unsupported ciphers and 2022 methods
// without a server key are errors, so the inbound is not built rather than
// built with users Xray cannot serve.
func ssMethod(cipher, serverKey string) (ssCipher, error) {
	name := strings.ToLower(strings.TrimSpace(cipher))
	switch name {
	case "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm":
		if serverKey == "" {
			return ssCipher{}, fmt.Errorf("shadowsocks %s needs server_key from the panel", name)
		}
		return ssCipher{name: name, is2022: true, keyLen: ss2022KeyLen(name)}, nil
	case "2022-blake3-chacha20-poly1305":
		return ssCipher{}, fmt.Errorf("shadowsocks %s serves a single user only; use 2022-blake3-aes-128-gcm or 2022-blake3-aes-256-gcm", name)
	}
	if t := getCipherFromString(name); t != shadowsocks.CipherType_UNKNOWN {
		return ssCipher{name: name, legacy: t}, nil
	}
	return ssCipher{}, fmt.Errorf("unsupported shadowsocks cipher %q; use one of: %s",
		cipher, strings.Join(SupportedShadowsocksCiphers, ", "))
}

// ss2022KeyLen is the key length of a Shadowsocks 2022 cipher.
func ss2022KeyLen(cipher string) int {
	if strings.EqualFold(cipher, "2022-blake3-aes-128-gcm") {
		return 16
	}
	return 32
}

func getCipherFromString(c string) shadowsocks.CipherType {
	switch strings.ToLower(c) {
	case "aes-128-gcm", "aead_aes_128_gcm":
		return shadowsocks.CipherType_AES_128_GCM
	case "aes-256-gcm", "aead_aes_256_gcm":
		return shadowsocks.CipherType_AES_256_GCM
	case "chacha20-poly1305", "aead_chacha20_poly1305", "chacha20-ietf-poly1305":
		return shadowsocks.CipherType_CHACHA20_POLY1305
	case "xchacha20-poly1305", "aead_xchacha20_poly1305", "xchacha20-ietf-poly1305":
		return shadowsocks.CipherType_XCHACHA20_POLY1305
	default:
		return shadowsocks.CipherType_UNKNOWN
	}
}

func buildHysteria2Users(tag string, userInfo []panel.UserInfo) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		users[i] = buildHysteria2User(tag, &userInfo[i])
	}
	return users
}

func buildHysteria2User(tag string, userInfo *panel.UserInfo) (user *protocol.User) {
	hysteria2Account := &hyaccount.Account{
		Auth: userInfo.Uuid,
	}
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(hysteria2Account),
	}
}

func buildTuicUsers(tag string, userInfo []panel.UserInfo) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		users[i] = buildTuicUser(tag, &userInfo[i])
	}
	return users
}

func buildTuicUser(tag string, userInfo *panel.UserInfo) (user *protocol.User) {
	tuicAccount := &tuic.Account{
		Uuid:     userInfo.Uuid,
		Password: userInfo.Uuid,
	}
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(tuicAccount),
	}
}

func buildAnyTLSUsers(tag string, userInfo []panel.UserInfo) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		users[i] = buildAnyTLSUser(tag, &userInfo[i])
	}
	return users
}

func buildAnyTLSUser(tag string, userInfo *panel.UserInfo) (user *protocol.User) {
	anyTLSAccount := &anytls.Account{
		Password: userInfo.Uuid,
	}
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(anyTLSAccount),
	}
}

func staticAccountProtocol(nodeType string) bool {
	switch nodeType {
	case "http", "mixed", "tunnel", "tun", "wireguard":
		return true
	default:
		return false
	}
}

func resolveUID(uidMap map[string]int, email string) int {
	if id := uidMap[email]; id != 0 {
		return id
	}
	if i := strings.LastIndex(email, "|"); i >= 0 {
		if id := uidMap[email[i+1:]]; id != 0 {
			return id
		}
	}
	return 0
}
