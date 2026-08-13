package node

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	log "github.com/sirupsen/logrus"
	panel "github.com/youtonghy/TJXT_node/api/v2board"
	"github.com/youtonghy/TJXT_node/common/crypt"
)

type mtprotoSidecar struct {
	cmd *exec.Cmd
}

func startMTProto(info *panel.NodeInfo, users []panel.UserInfo) (*mtprotoSidecar, error) {
	if len(users) == 0 {
		return nil, fmt.Errorf("mtproto inbound refuses an empty user list")
	}
	listen := info.Common.ListenIP
	if listen == "" {
		listen = "0.0.0.0"
	}
	port := strconv.Itoa(info.Common.ServerPort)
	domain := strings.TrimSpace(info.Common.ServerName)
	if domain == "" {
		domain = jsonStringSetting(info, "serverName")
	}
	secrets := make([]string, 0, len(users))
	for _, user := range users {
		secrets = append(secrets, crypt.MTProtoSecret(user.Uuid, domain))
	}
	bind := listen + ":" + port
	if path, err := exec.LookPath("mtproto-proxy"); err == nil {
		args := []string{"-u", "nobody", "-p", "8888", "-H", port}
		for _, secret := range secrets {
			args = append(args, "-S", normalizedMTProxySecret(secret))
		}
		cmd := exec.Command(path, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start mtproto-proxy: %w", err)
		}
		log.WithField("bind", bind).Infof("mtproto-proxy sidecar started with %d secrets", len(secrets))
		return &mtprotoSidecar{cmd: cmd}, nil
	}
	if path, err := exec.LookPath("mtg"); err == nil {
		if len(secrets) > 1 {
			log.Warn("mtg only accepts one secret; extra users will not authenticate until mtproto-proxy is installed")
		}
		cmd := exec.Command(path, "simple-run", bind, secrets[0])
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start mtg: %w", err)
		}
		log.WithField("bind", bind).Info("mtg sidecar started")
		return &mtprotoSidecar{cmd: cmd}, nil
	}
	return nil, fmt.Errorf("mtproto requires mtg or mtproto-proxy on PATH")
}

func (s *mtprotoSidecar) Stop() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	_ = s.cmd.Wait()
}

func jsonStringSetting(info *panel.NodeInfo, key string) string {
	if info == nil || info.Common == nil || len(info.Common.Settings) == 0 {
		return ""
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(info.Common.Settings, &object); err != nil {
		return ""
	}
	raw, ok := object[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return ""
}

func normalizedMTProxySecret(secret string) string {
	secret = strings.TrimPrefix(secret, "ee")
	secret = strings.TrimPrefix(secret, "dd")
	if len(secret) >= 32 {
		return secret[:32]
	}
	return secret
}
