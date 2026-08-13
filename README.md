# TJXT_node

Xray node agent for TONG-ladder. It does not store users. The panel is the source of truth.

## Protocol

Primary: `POST /api/v3/server` with `{endpoint, method, params}` (UniProxy actions).

Fallback: `/api/v2/server/config` and `/api/v1/server/UniProxy/{user,push,alive,alivelist}`.

Set `Protocol` to `auto` (default), `v3ui`, or `v1`.

Authenticate with `X-Server-Token: <per-node server_key>`. Do not use a user or admin session token.

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

## Build

```bash
GOEXPERIMENT=jsonv2 go build -o build/tjxt-node .
```

This tree is derived from [v2node](https://github.com/wyx2685/v2node) (MPL-2.0): embedded Xray, stream transport, and a custom dispatcher interlayer for limits and traffic.

## Runtime coverage

Xray inbound builders start:

- `vmess`, `vless`, `trojan`, `shadowsocks`, `hysteria2`, `tuic`, `anytls`
- `wireguard` (client keys derived from uuid)
- `http` and `mixed` (uuid/uuid basic auth; noauth is refused)
- `tunnel` (dokodemo-door)
- `tun` (no host route rewrite)

`mtproto` runs `mtproto-proxy` (multi-secret) or `mtg` (single secret) as a sidecar. Install one of those binaries on PATH.

WireGuard and MTProto secrets stay derived from the panel user uuid so the user list remains `{id, uuid, speed_limit, device_limit}`.
