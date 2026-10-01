# TJXT_node

Xray node agent for TONG-ladder. It does not store users. The panel is the source of truth.

## Protocol

Primary: `POST /api/v3/server` with `{endpoint, method, params}` (UniProxy actions).

Fallback: `/api/v2/server/config` and `/api/v1/server/UniProxy/{user,push,alive,alivelist}`.

Set `Protocol` to `auto` (default), `v3ui`, or `v1`.

Authenticate with `X-Server-Token: <per-node server_key>` only; the key is never put in URLs or bodies. Do not use a user or admin session token.

- Traffic is pushed in batches with a stable `batch_id` (v1 also sends `X-Traffic-Batch-Id`). A batch is resent unchanged until the panel accepts it, and only then subtracted from the counters, so failed pushes and reloads do not lose traffic.
- An empty user list is valid: the inbound stays down and comes up when users appear. A 304 means unchanged.
- Private, loopback, link-local and metadata destinations are blocked unless the panel sets `base_config.allow_private_egress: true` for the node.
- `routes` are applied in panel order; a route whose outbound Xray rejects is skipped and logged.

## Config

```json
{
  "Log": { "Level": "info", "Output": "", "Access": "none" },
  "Nodes": [
    {
      "ApiHost": "http://127.0.0.1:3000",
      "NodeID": 1,
      "ApiKey": "per-node-server-key",
      "Protocol": "auto"
    }
  ]
}
```

## Deploy

VPS 一键安装（systemd / OpenRC）：下载最新 Release 的二进制和 `deploy.sh`，按 `SHA256SUMS` 校验后安装，并写入 `/etc/tjxt-node/config.json`。面板后台节点列表的“安装命令”会生成同样的命令：

```bash
curl -fsSL https://raw.githubusercontent.com/youtonghy/TJXT_node/main/deploy.sh \
  | sudo bash -s -- --api-host https://panel.example.com --node-id 1 --api-key 'your-server-key'
```

- 仓库私有时匿名下载会失败：先 `export GITHUB_TOKEN=<有 repo 读权限的 token>`，脚本改走 GitHub API；脚本本身可用 `curl -H "Authorization: Bearer $GITHUB_TOKEN" …` 获取。
- `--version vX.Y.Z` 安装指定版本；`TJXT_RELEASE_BASE_URL` 可指向提供相同文件名的镜像。
- `--build`（或 `--source DIR`、在克隆的仓库里运行 `./deploy.sh`）改为从源码编译，需要 Go 1.27；国内可加 `GOPROXY=https://goproxy.cn,direct`。
- 服务以系统用户 `tjxt-node` 运行，只有 `CAP_NET_BIND_SERVICE` 和 `CAP_NET_ADMIN`（TUN）。证书路径请放在 `/etc/tjxt-node` 下，私钥权限为 0600。`update` 会把旧的 root 服务迁移到该用户。

安装后：

```bash
sudo tjxt-node-deploy status
sudo tjxt-node-deploy log
sudo tjxt-node-deploy restart
sudo tjxt-node-deploy update
```

仅本地编译、不装服务：`./deploy.sh local`。节点监听端口由面板下发，脚本不会改防火墙。

## Release

Push a `v*` tag. `.github/workflows/release.yml` runs vet and race tests, builds `tjxt-node-linux-{amd64,arm64}`, and publishes them with `deploy.sh` and `SHA256SUMS`.

## Build

```bash
GOEXPERIMENT=jsonv2 go build -o build/tjxt-node .
```

Requires Go 1.27. Xray comes from [wyx2685/xray-core](https://github.com/wyx2685/xray-core) (upstream v26.9.30 plus anytls/tuic), pinned by commit in the `replace` directive of `go.mod`.

This tree is derived from [v2node](https://github.com/wyx2685/v2node) (MPL-2.0): embedded Xray, stream transport, and a custom dispatcher interlayer for limits and traffic.

## Runtime coverage

Xray inbound builders start:

- `vmess`, `vless`, `trojan`, `shadowsocks`, `hysteria2`, `tuic`, `anytls`
- `wireguard` (client keys derived from uuid)
- `http` and `mixed` (uuid/uuid basic auth; noauth is refused)
- `tunnel` (dokodemo-door)
- `tun` (no host route rewrite)

Shadowsocks 2022 takes its server PSK from the config's `server_key`; user keys are the first key-length bytes of the uuid. REALITY on current Xray only accepts clients that send an X25519MLKEM768 key share. A node whose inbound cannot be built is logged and retried on every poll without stopping the other nodes. The panel repo's `scripts/docker_node_e2e.py` exercises every protocol and route action with real clients in Docker.

`mtproto` is retired: the panel no longer creates such nodes and the agent refuses their config.

WireGuard keys stay derived from the panel user uuid so the user list remains `{id, uuid, speed_limit, device_limit}`.
