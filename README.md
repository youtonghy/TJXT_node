# TJXT_node

Xray node agent for TONG-ladder. It does not store users. The panel is the source of truth.

## Protocol

Primary: `POST /api/v3/server` with `{endpoint, method, params}` (UniProxy actions).

Fallback: `/api/v2/server/config` and `/api/v1/server/UniProxy/{user,push,alive,alivelist}`.

Set `Protocol` to `auto` (default), `v3ui`, or `v1`.

Authenticate with `X-Server-Token: <machine key or per-node server_key>` only; the key is never put in URLs or bodies. Do not use a user or admin session token.

- **Machine pairing.** A machine key (from the panel's machine list) pairs the whole server once. The agent polls `GET /api/v1/server/UniProxy/machine` (v3 endpoint `server/UniProxy/machine`, with ETag) for `{machine_id, name, pull_interval, nodes:[{id, name, protocol}]}` and runs every listed node; each node's config, users, traffic and online IPs use the same machine key with that node's `node_id`, which the panel accepts for the machine's nodes only. When the panel adds a node to the machine or removes one, the agent starts or stops just that node within `pull_interval` (the smallest of the machine's nodes, default 60 s), without restarting the Xray core, as long as the core's outbounds and routing stay the same; otherwise (new routes or egress rules) it reloads every node. A failed or rejected poll keeps the running nodes. A panel without the machine endpoint (HTTP 404) is reported as not supporting machine pairing.

- **Pairing by key.** A node without `NodeID` sends no `node_id`/`server_id`; the panel finds the node from its own key and reports it as `base_config.node_id` (the top-level `id` is not trusted). That id is used for tags, certificate file names and logs; requests keep identifying the node by its key. If the answer has no `base_config.node_id`, the key is the fleet-wide server token and the node fails with "this key is not a node pairing key". A configured `NodeID` is still sent, and a different `base_config.node_id` is an error.
- A rejected key is HTTP 401/403 or any answer whose body has `"error_code": "node_unauthorized"` (the panel uses 400). It is logged as a possibly rotated key and retried; traffic batches are kept, not dropped.

- Traffic is pushed in batches with a stable `batch_id` (v1 also sends `X-Traffic-Batch-Id`). A batch is resent unchanged until the panel accepts it, and only then subtracted from the counters, so failed pushes and reloads do not lose traffic. When the counters go away before the panel accepts them (the node stops, users are removed, the panel is unreachable), the traffic is written to `/etc/tjxt-node/spool` (`TJXT_SPOOL_DIR` overrides it) and reported first on the next push, under its original batch IDs.
- A Hysteria 2 node with `hop_ports` (e.g. `20000-30000`) gets an nftables table of its own that redirects those UDP ports to its listen port while the inbound runs (Linux, `nft` installed — `deploy.sh` installs nftables; the service has `CAP_NET_ADMIN`). Without nftables the node logs a warning and clients still use the listen port.
- A TLS node reports the hex SHA-256 of its certificate in `X-TJXT-Cert-SHA256` (after the certificate is created or renewed), so the panel can let clients pin a self-signed certificate.
- A push answer may list `revoked_user_ids` (quota used up, package ended): those users are disconnected at once and stay removed until the panel sends a changed user list.
- The user list is fetched in pages of 500 on v3 (the panel's gateway refuses responses over 1 MiB). The ETag is reused only when the whole list fits in one page; a longer list is fetched in full on every poll, since the panel's ETag covers one page.
- The node reports its online IPs (`alive`) on every push interval, also when nobody is online, so the panel drops users who went offline. It does not act on the `device_limit_exceeded` list in the answer: new IPs over a user's limit are already refused from the fleet-wide counts of `alivelist`.
- Online devices are the IPs whose connections carried traffic since the previous report, so long-lived connections stay counted. At the device limit, an IP active on any inbound of the node within five minutes is not a new device.
- **Shared inbounds.** A node whose config has `shared_inbound: <id>` is a line of node `<id>`'s inbound: same port, protocol, transport and TLS/REALITY. The agent runs one Xray inbound for the node and its lines; each node's users join it with their own credentials and their emails (`<node tag>|<credential>`) carry the node's tag, so traffic, speed and device limits and online IPs are kept per node, and each node's `routes` (and private egress) match its users with `user: ["regexp:^<node tag>\\|"]` instead of `inboundTag`. The inbound runs while any of these nodes has users; adding or removing a line reloads all nodes. A line uses the certificate of the node it shares and waits for it. The agent announces `shared-inbound` in `X-TJXT-Features`; the panel lists lines only to agents that do.
- Two nodes that listen on the same port (same TCP/UDP kind, overlapping listen address) without sharing an inbound cannot both run: Xray opens listeners with `SO_REUSEPORT`, which would split connections between them. Only the node with the lowest id starts; the other is logged as conflicting.
- A config change that touches only one node's inbound (protocol, transport, TLS/REALITY, port, ...) rebuilds that inbound alone; the other nodes in the process keep their connections. Changes to routes, `base_config` (private egress, intervals) or the node's tag restart the Xray core, as before.
- An empty user list is valid: the inbound stays down and comes up when users appear. A 304 means unchanged.
- Private, loopback, link-local and metadata destinations are blocked unless the panel sets `base_config.allow_private_egress: true` for the node.
- `routes` carry Xray JSON outbound/routing rules (`rule`) and are stacked and applied in panel order; a rule whose outbound or routing rule Xray rejects is skipped and logged.

## Config

```json
{
  "Log": { "Level": "info", "Output": "", "Access": "none" },
  "Machine": {
    "ApiHost": "http://127.0.0.1:3000",
    "ApiKey": "this-machine-key",
    "Protocol": "auto",
    "Timeout": 15
  }
}
```

`Machine` pairs the server once; the nodes come from the panel. Nodes can also be configured one by one, alone or next to the machine (a node in both runs once, as the machine's):

```json
"Nodes": [
  { "ApiHost": "http://127.0.0.1:3000", "ApiKey": "this-node-own-key", "Protocol": "auto", "Timeout": 15 }
]
```

The config needs a `Machine` or at least one node. `ApiHost` and `ApiKey` are required. `NodeID` is optional: leave it out (or 0) to pair by key, which needs the node's own key from the panel's node list. Old configs with `NodeID` (including ones using the global server token) work unchanged.

Environment variables, e.g. for Docker: when `TJXT_API_HOST` and `TJXT_API_KEY` are both set they **replace** the file's `Nodes` with that one node (`TJXT_NODE_ID` optional, timeout 15 s, protocol `auto`). The file's other settings (`Log`, `PprofPort`) still apply, and the file may be missing. Setting only one of the two, or `TJXT_NODE_ID` alone, is an error. `TJXT_API_HOST` with `TJXT_MACHINE_KEY` instead sets the `Machine` and clears the file's `Nodes`; it cannot be combined with `TJXT_API_KEY` or `TJXT_NODE_ID`.

### Pairing

```bash
TJXT_API_KEY='this-machine-or-node-key' tjxt-node pair --api-host https://panel.example.com
```

Without `--node-id`, `pair` first asks the machine endpoint. A machine key prints the machine id, name and its nodes (`Key belongs to machine N (name "...")`, `Machine runs N nodes`, `Machine node ID (type T, name "...")`) and writes `Machine` into `-c`: nodes of `Nodes` the machine runs (same panel, a listed id, or the machine key itself) are dropped; with `--append` the other nodes stay, otherwise `Nodes` is removed (several nodes need `--force`). Any other key is paired as a node key, as below.

For a node key, `pair` fetches the node config with the key, prints the node id, type, name and address (`host:port`, or the port when the panel sets no host), and writes `-c` (default `/etc/tjxt-node/config.json`) with that single node and its `NodeID`. An existing config keeps its other settings (`Log`, `PprofPort`, ...) and its `Nodes` are replaced; a config with several nodes, or one that does not parse, is only replaced with `--force`. The file is written atomically with mode 0600 (directory 0750). Other flags: `--key` (default `$TJXT_API_KEY`; the environment keeps the key out of the process list), `--protocol auto|v3ui|v1`, `--timeout 15`, `--node-id N` (check a legacy key against a known id), `--dry-run` (check only, write nothing). The key is never printed.

Exit codes: 0 paired, 1 other panel error, 2 invalid flags or URL, 3 panel unreachable, 4 key rejected, 5 global server token instead of a node key, 6 config not written, 7 the address does not serve the node API (HTTP 404 or a non-JSON reply), 8 the panel's config for this node cannot be used (a field this version cannot read, or an unsupported protocol).

### Startup

Only a config file that does not load or validate stops `tjxt-node server` (exit code 1). Each node's config is fetched concurrently; a node whose panel is unreachable or rejects its key is logged and retried with backoff from 1 s to 60 s while the other nodes run. When its config arrives the server reloads and starts it. If no node gets a config the process stays up and keeps retrying. On a reload, a running node whose panel cannot be reached keeps its last config.

## Deploy

VPS 一键安装（systemd / OpenRC）：下载最新 Release 的二进制和 `deploy.sh`，按 `SHA256SUMS` 校验后安装，写入 `/etc/tjxt-node/config.json`，并把服务设为开机自启后启动。只需面板地址和密钥：推荐使用面板“服务器”页面的服务器密钥，一台服务器只配对一次，之后在面板中给该服务器增删节点会自动同步，无需再登录服务器；也可以用节点自己的密钥单独配对一个节点，节点 ID 由面板根据密钥识别。

交互安装：不带参数运行，脚本依次询问面板地址和密钥（密钥输入不显示）。

```bash
curl -fsSL https://raw.githubusercontent.com/youtonghy/TJXT_node/main/deploy.sh | sudo bash
```

- 面板地址可以只写域名（`panel.example.com`），也可以直接粘贴后台页面地址；脚本会去掉空白、引号、`?`/`#` 之后的部分、`/api/...` 和末尾的 `/`，没写协议时补 `https://`。校验时依次尝试该地址、去掉页面路径后的地址，以及（没写协议时）对应的 `http://` 地址，并使用第一个能通过校验的地址。
- 校验通过后会显示面板返回的信息（服务器密钥：服务器 ID、名称和当前节点；节点密钥：节点 ID、类型、名称和地址），确认后才写配置并启动服务；校验失败会说明原因（连不上面板、密钥被拒、填了全局密钥）并允许重新填写。
- 安装结束时会显示开机自启和运行状态。
- 已有配置时会询问追加（保留已有节点）还是替换；不填密钥则保留原配置。

非交互安装：面板后台“服务器”页面（或节点列表）的“安装命令”会生成这样的命令，校验后直接显示信息并完成安装：

```bash
curl -fsSL https://raw.githubusercontent.com/youtonghy/TJXT_node/main/deploy.sh \
  | sudo bash -s -- --api-host https://panel.example.com --api-key 'this-machine-key'
```

- 已有配置且给了密钥时，非交互安装默认追加（`--node-action append`）：服务器密钥会加入配置并去掉它已接管的节点，节点密钥会追加为新节点；需要清空旧配置时指定 `--node-action replace`。

- 新装或 `--force-config` 时，脚本先用 `tjxt-node pair` 向面板校验地址和密钥，并把面板报告的节点 ID 写入配置；校验失败会打印原因，不写配置、不启动服务。已有配置保持不变。
- `--node-id N` 仍可用（旧的全局密钥需要它），给出时会写入配置并与面板核对。

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

Push a `v*` tag, or run the Release workflow manually (Actions → Release → Run workflow) with a version such as `0.1.0` or `v0.1.0` (the `v` prefix is added when missing); the manual run creates the `v` tag on the selected branch's commit. `.github/workflows/release.yml` runs vet and race tests, builds `tjxt-node-linux-{amd64,arm64}`, and publishes them with `deploy.sh` and `SHA256SUMS`.

## Upgrading Xray

Xray comes from the `youtonghy/xray-core` fork (`replace` in `go.mod`). On each upgrade:

1. Merge only a **stable** upstream release tag into the fork. Upstream ships many pre-releases without notes and has withdrawn releases (v26.10.10 and v26.10.17 bundled compromised root CAs); never track them.
2. Keep the REALITY pin (`github.com/xtls/reality` at `e1986a4d31ca`, before X25519MLKEM768 became mandatory) until sing-box sends an X25519MLKEM768 key share (sing-box issue #4520); mihomo already does with `support-x25519mlkem768`, which the panel sets. Drop the pin only after the Docker E2E passes with the sing-box REALITY client.
3. Point `go.mod` at the new fork commit, then run `gofmt -l`, `go vet ./...` and `go test -race -count=1 ./...`. `TestPanelPresetsStartXray` starts every panel preset (`node/testdata/presets`, regenerated from TONG-ladder) with the new core; a failure means the release changed a config the panel produces.
4. Run TONG-ladder's `python3 scripts/docker_node_e2e.py` with `TJXT_NODE_ROOT` pointing here: real clients through every protocol and route action.
5. Read the release notes for removed or renamed config fields (e.g. `allowInsecure` became an error in 26.x) and adjust the panel's subscriptions in the same task.

## Build

```bash
GOEXPERIMENT=jsonv2 go build -o build/tjxt-node .
```

Requires Go 1.27. Xray comes from [youtonghy/xray-core](https://github.com/youtonghy/Xray-core), a fork of [wyx2685/xray-core](https://github.com/wyx2685/xray-core) (anytls/tuic/hysteria2) merged with upstream v26.10.21, pinned by commit in the `replace` directive of `go.mod`. Its REALITY adapter keeps the API of the pinned REALITY library so sing-box and mihomo clients can connect.

This tree is derived from [v2node](https://github.com/wyx2685/v2node) (MPL-2.0): embedded Xray, stream transport, and a custom dispatcher interlayer for limits and traffic.

## Runtime coverage

Xray inbound builders start:

- `vmess`, `vless`, `trojan`, `shadowsocks`, `hysteria2`, `tuic`, `anytls`
- `wireguard` (client keys derived from uuid)
- `http` and `mixed` (uuid/uuid basic auth; noauth is refused)
- `tunnel` (dokodemo-door)
- `tun` (no host route rewrite)

Transports (`network`) of `vless`, `vmess`, `trojan` and `anytls`: `tcp` (also `raw`, and the default when empty), `ws`, `grpc`, `httpupgrade`, `xhttp` (also `splithttp`). Xray removed the HTTP/2 (`http`, `h2`) and QUIC transports; another network fails the inbound with an error naming it. Missing, `null` or `{}` `network_settings` keep the transport's defaults.

TLS: `trojan` and `anytls` are always served over TLS (`"tls": 0` counts as TLS; REALITY, `"tls": 2`, is kept); `hysteria2` and `tuic` always use TLS. With TLS on and no `cert_mode`, these four get a self-signed certificate (`self`); `hysteria2` and `tuic` also for `none`, since QUIC cannot be terminated in front of the node. `vless` and `vmess` with `"tls": 1` need a `cert_mode`: an empty one fails the config instead of serving plaintext, and `none` serves plaintext for TLS terminated in front of the node (a CDN or reverse proxy). A self-signed certificate is made for `tls_settings.server_name`, else the node `host` (an IP goes into the IP SANs); clients pin it by fingerprint.

Shadowsocks ciphers: `aes-128-gcm`, `aes-256-gcm`, `chacha20-poly1305` (`chacha20-ietf-poly1305`), `xchacha20-poly1305` (`xchacha20-ietf-poly1305`), `2022-blake3-aes-128-gcm`, `2022-blake3-aes-256-gcm`. Another cipher, `2022-blake3-chacha20-poly1305` included (Xray serves it to a single user only), fails the inbound with an error. Shadowsocks 2022 takes its server PSK from the config's `server_key` and fails without it; user keys are the first key-length bytes of the uuid. `github.com/xtls/reality` is pinned before the change that rejects clients without an X25519MLKEM768 key share, so sing-box and mihomo can use REALITY (see the comment in `go.mod`). A node whose inbound cannot be built is logged and retried on every poll without stopping the other nodes. The panel repo's `scripts/docker_node_e2e.py` exercises every protocol and route action with real clients in Docker.

`mtproto` is retired: the panel no longer creates such nodes and the agent refuses their config.

WireGuard keys stay derived from the panel user uuid so the user list remains `{id, uuid, speed_limit, device_limit}`.
