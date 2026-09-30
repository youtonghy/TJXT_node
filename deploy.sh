#!/usr/bin/env bash
# TJXT_node one-click deploy for TONG-ladder.
# Builds from source (GOEXPERIMENT=jsonv2) and installs a systemd/openrc service.
set -euo pipefail

readonly APP_NAME="tjxt-node"
readonly DEFAULT_REPO="https://github.com/youtonghy/TJXT_node.git"
readonly DEFAULT_BRANCH="main"
readonly DEFAULT_GO_VERSION="1.26.1"
readonly DEFAULT_PREFIX="/usr/local/tjxt-node"
readonly DEFAULT_CONFIG_DIR="/etc/tjxt-node"
readonly DEFAULT_SRC="/usr/local/src/TJXT_node"
readonly DEFAULT_PROTOCOL="auto"
readonly SERVICE_NAME="tjxt-node"
LOG_FILE="${TJXT_LOG_FILE:-/var/log/${APP_NAME}/node.log}"

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
cyan='\033[0;36m'
plain='\033[0m'

CMD=""
API_HOST="${TJXT_API_HOST:-}"
NODE_ID="${TJXT_NODE_ID:-}"
API_KEY="${TJXT_API_KEY:-}"
PROTOCOL="${TJXT_PROTOCOL:-$DEFAULT_PROTOCOL}"
BRANCH="${TJXT_BRANCH:-$DEFAULT_BRANCH}"
REPO="${TJXT_REPO:-$DEFAULT_REPO}"
PREFIX="${TJXT_PREFIX:-$DEFAULT_PREFIX}"
CONFIG_DIR="${TJXT_CONFIG_DIR:-$DEFAULT_CONFIG_DIR}"
SRC_OVERRIDE="${TJXT_SRC:-}"
FORCE_CONFIG=0
NO_START=0
YES=0
PURGE=0
SRC=""

log()  { printf "${green}[+]${plain} %s\n" "$*"; }
warn() { printf "${yellow}[!]${plain} %s\n" "$*" >&2; }
err()  { printf "${red}[x]${plain} %s\n" "$*" >&2; }
die()  { err "$*"; exit 1; }

usage() {
  cat <<'EOF'
TJXT_node 一键部署（TONG-ladder 节点）

用法:
  sudo ./deploy.sh --api-host https://panel.example.com --node-id 1 --api-key KEY
  curl -fsSL https://raw.githubusercontent.com/youtonghy/TJXT_node/main/deploy.sh \
    | sudo bash -s -- --api-host URL --node-id 1 --api-key KEY

子命令:
  install      安装或更新（默认）
  update       拉取源码并更新二进制
  start|stop|restart|status|log
  uninstall    卸载服务和二进制（默认保留配置）
  local        仅本地编译，不安装 systemd
  menu         交互菜单

选项:
  --api-host URL            面板地址，例如 https://panel.example.com
  --node-id N               面板节点 ID
  --api-key KEY             节点 server_key（X-Server-Token）
  --protocol auto|v3ui|v1   对接协议，默认 auto
  --branch NAME             源码分支，默认 main
  --repo URL                Git 仓库地址
  --source DIR              使用已有源码目录
  --prefix DIR              安装目录，默认 /usr/local/tjxt-node
  --config-dir DIR          配置目录，默认 /etc/tjxt-node
  --force-config            覆盖已有配置
  --no-start                安装后不启动服务
  --yes                     卸载时跳过确认
  --purge                   卸载时同时删除配置和日志
  -h, --help                显示帮助

环境变量: TJXT_API_HOST TJXT_NODE_ID TJXT_API_KEY TJXT_PROTOCOL GOPROXY
国内编译可设: GOPROXY=https://goproxy.cn,direct

本脚本不会关闭防火墙、不会上报统计。节点监听端口由面板下发，请自行放行。
EOF
}

is_tty() { [[ -t 0 && -t 1 ]]; }

need_root() {
  [[ "${EUID:-$(id -u)}" -eq 0 ]] || die "请使用 root 运行（sudo $0 $*）"
}

os_id() {
  local sys
  sys="$(uname -s | tr '[:upper:]' '[:lower:]')"
  printf '%s' "$sys"
}

go_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64' ;;
    aarch64|arm64) printf 'arm64' ;;
    *) die "不支持的架构: $(uname -m)" ;;
  esac
}

json_escape() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}
  s=${s//$'\r'/\\r}
  s=${s//$'\t'/\\t}
  printf '%s' "$s"
}

version_ge() {
  # version_ge 1.26.1 1.26  -> true if $1 >= $2
  local IFS=.
  local i
  local -a a=($1) b=($2)
  for ((i = 0; i < ${#b[@]}; i++)); do
    (( ${a[i]:-0} > ${b[i]:-0} )) && return 0
    (( ${a[i]:-0} < ${b[i]:-0} )) && return 1
  done
  return 0
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      install|update|start|stop|restart|status|log|uninstall|local|menu)
        CMD="$1"
        shift
        ;;
      --api-host)
        API_HOST="${2:-}"; shift 2 ;;
      --api-host=*)
        API_HOST="${1#*=}"; shift ;;
      --node-id)
        NODE_ID="${2:-}"; shift 2 ;;
      --node-id=*)
        NODE_ID="${1#*=}"; shift ;;
      --api-key)
        API_KEY="${2:-}"; shift 2 ;;
      --api-key=*)
        API_KEY="${1#*=}"; shift ;;
      --protocol)
        PROTOCOL="${2:-}"; shift 2 ;;
      --protocol=*)
        PROTOCOL="${1#*=}"; shift ;;
      --branch)
        BRANCH="${2:-}"; shift 2 ;;
      --branch=*)
        BRANCH="${1#*=}"; shift ;;
      --repo)
        REPO="${2:-}"; shift 2 ;;
      --repo=*)
        REPO="${1#*=}"; shift ;;
      --source)
        SRC_OVERRIDE="${2:-}"; shift 2 ;;
      --source=*)
        SRC_OVERRIDE="${1#*=}"; shift ;;
      --prefix)
        PREFIX="${2:-}"; shift 2 ;;
      --prefix=*)
        PREFIX="${1#*=}"; shift ;;
      --config-dir)
        CONFIG_DIR="${2:-}"; shift 2 ;;
      --config-dir=*)
        CONFIG_DIR="${1#*=}"; shift ;;
      --force-config)
        FORCE_CONFIG=1; shift ;;
      --no-start)
        NO_START=1; shift ;;
      --yes|-y)
        YES=1; shift ;;
      --purge)
        PURGE=1; shift ;;
      -h|--help)
        usage; exit 0 ;;
      *)
        die "未知参数: $1  （$0 --help）"
        ;;
    esac
  done
  CMD="${CMD:-}"
  case "$PROTOCOL" in
    auto|v3ui|v1) ;;
    *) die "--protocol 只能是 auto、v3ui 或 v1" ;;
  esac
}

script_dir() {
  local self="${BASH_SOURCE[0]:-}"
  if [[ -n "$self" && -f "$self" ]]; then
    (cd "$(dirname "$self")" && pwd)
  fi
}

ensure_commands() {
  local missing=()
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || missing+=("$c")
  done
  ((${#missing[@]})) && die "缺少命令: ${missing[*]}"
}

install_build_deps() {
  local pkgs=(git curl tar ca-certificates)
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -y
    DEBIAN_FRONTEND=noninteractive apt-get install -y "${pkgs[@]}"
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y git curl tar ca-certificates
  elif command -v yum >/dev/null 2>&1; then
    yum install -y git curl tar ca-certificates
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache git curl tar ca-certificates
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Sy --noconfirm git curl tar ca-certificates
  else
    ensure_commands git curl tar
  fi
}

go_version_ok() {
  command -v go >/dev/null 2>&1 || return 1
  local raw
  raw="$(go version 2>/dev/null | awk '{print $3}')"
  raw="${raw#go}"
  [[ -n "$raw" ]] || return 1
  version_ge "$raw" "1.26"
}

ensure_go() {
  if go_version_ok; then
    log "使用已有 Go: $(go version)"
    return
  fi
  local os arch tarball tmp dest
  os="$(os_id)"
  [[ "$os" == "darwin" || "$os" == "linux" ]] || die "请先安装 Go >= 1.26"
  arch="$(go_arch)"
  tarball="go${DEFAULT_GO_VERSION}.${os}-${arch}.tar.gz"
  dest="/usr/local/go"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  log "安装 Go ${DEFAULT_GO_VERSION} 到 ${dest}"
  local url ok=0
  for url in \
    "https://go.dev/dl/${tarball}" \
    "https://mirrors.aliyun.com/golang/${tarball}" \
    "https://golang.google.cn/dl/${tarball}"
  do
    log "下载 $url"
    if curl -fL --retry 3 --retry-delay 2 -o "$tmp/$tarball" "$url"; then
      ok=1
      break
    fi
  done
  ((ok)) || die "下载 Go 失败，请手动安装 Go ${DEFAULT_GO_VERSION}+"
  rm -rf "$dest"
  tar -C /usr/local -xzf "$tmp/$tarball"
  export PATH="/usr/local/go/bin:$PATH"
  go_version_ok || die "Go 安装后仍不可用"
  log "$(go version)"
}

clone_or_update_src() {
  local dest=$1
  mkdir -p "$(dirname "$dest")"
  if [[ -d "$dest/.git" ]]; then
    log "更新源码 $dest ($BRANCH)"
    git -C "$dest" remote set-url origin "$REPO" 2>/dev/null || true
    git -C "$dest" fetch --depth 1 origin "$BRANCH"
    git -C "$dest" checkout -B "$BRANCH" "FETCH_HEAD"
  else
    log "克隆 $REPO ($BRANCH) -> $dest"
    rm -rf "$dest"
    git clone --depth 1 --branch "$BRANCH" "$REPO" "$dest"
  fi
}

resolve_source() {
  if [[ -n "$SRC_OVERRIDE" ]]; then
    [[ -f "$SRC_OVERRIDE/go.mod" ]] || die "--source 目录没有 go.mod: $SRC_OVERRIDE"
    SRC="$(cd "$SRC_OVERRIDE" && pwd)"
    return
  fi
  local dir
  dir="$(script_dir || true)"
  if [[ -n "$dir" && -f "$dir/go.mod" ]]; then
    SRC="$dir"
    return
  fi
  clone_or_update_src "$DEFAULT_SRC"
  SRC="$DEFAULT_SRC"
  if [[ -f "$SRC/deploy.sh" && "$(cd "$dir" 2>/dev/null && pwd || true)" != "$SRC" ]]; then
    log "改用仓库内 deploy.sh 继续"
    exec bash "$SRC/deploy.sh" "$@"
  fi
}

binary_path() { printf '%s/%s' "$PREFIX" "$APP_NAME"; }
config_path() { printf '%s/config.json' "$CONFIG_DIR"; }
log_path()    { printf '%s' "$LOG_FILE"; }

has_systemd() {
  command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]
}

has_openrc() {
  command -v rc-update >/dev/null 2>&1 && [[ -d /etc/init.d ]]
}

prompt_install_fields() {
  if [[ -n "$API_HOST" && -n "$NODE_ID" && -n "$API_KEY" ]]; then
    return
  fi
  local cfg
  cfg="$(config_path)"
  if [[ -f "$cfg" && "$FORCE_CONFIG" -eq 0 ]]; then
    log "已有配置 $cfg，跳过填写（需要覆盖请加 --force-config）"
    return
  fi
  is_tty || die "缺少 --api-host --node-id --api-key。非交互安装必须提供这三项。"
  printf "${cyan}对接 TONG-ladder 面板${plain}\n"
  if [[ -z "$API_HOST" ]]; then
    read -r -p "面板地址 ApiHost: " API_HOST
  fi
  if [[ -z "$NODE_ID" ]]; then
    read -r -p "节点 ID NodeID: " NODE_ID
  fi
  if [[ -z "$API_KEY" ]]; then
    read -r -p "节点密钥 ApiKey (server_key): " API_KEY
  fi
  if [[ "$PROTOCOL" == "$DEFAULT_PROTOCOL" ]]; then
    local proto
    read -r -p "协议 Protocol [auto]: " proto
    PROTOCOL="${proto:-$DEFAULT_PROTOCOL}"
  fi
  [[ -n "$API_HOST" && -n "$NODE_ID" && -n "$API_KEY" ]] || die "ApiHost / NodeID / ApiKey 不能为空"
  [[ "$NODE_ID" =~ ^[0-9]+$ ]] || die "NodeID 必须是数字"
}

write_config() {
  local cfg dest_dir
  cfg="$(config_path)"
  dest_dir="$(dirname "$cfg")"
  mkdir -p "$dest_dir" "$(dirname "$(log_path)")"
  if [[ -f "$cfg" && "$FORCE_CONFIG" -eq 0 ]]; then
    log "保留已有配置 $cfg"
    return
  fi
  [[ -n "$API_HOST" && -n "$NODE_ID" && -n "$API_KEY" ]] || die "写配置需要 --api-host --node-id --api-key"
  [[ "$NODE_ID" =~ ^[0-9]+$ ]] || die "NodeID 必须是数字"
  local host key proto logf
  host="$(json_escape "$API_HOST")"
  key="$(json_escape "$API_KEY")"
  proto="$(json_escape "$PROTOCOL")"
  logf="$(json_escape "$(log_path)")"
  umask 077
  cat >"$cfg" <<EOF
{
  "Log": {
    "Level": "info",
    "Output": "${logf}",
    "Access": "none"
  },
  "Nodes": [
    {
      "ApiHost": "${host}",
      "NodeID": ${NODE_ID},
      "ApiKey": "${key}",
      "Protocol": "${proto}",
      "Timeout": 15
    }
  ]
}
EOF
  chmod 600 "$cfg"
  log "写入配置 $cfg"
}

build_binary() {
  ensure_commands git
  [[ -f "$SRC/go.mod" ]] || die "源码不完整: $SRC"
  export CGO_ENABLED=0
  export GOEXPERIMENT=jsonv2
  export GOPROXY="${GOPROXY:-https://goproxy.cn,https://proxy.golang.org,direct}"
  export PATH="/usr/local/go/bin:${PATH}"
  local version out
  version="$(git -C "$SRC" describe --tags --always --dirty 2>/dev/null || echo dev)"
  out="$(mktemp -d)/${APP_NAME}"
  log "编译 ${APP_NAME}（GOEXPERIMENT=jsonv2, version=${version}）"
  (
    cd "$SRC"
    go build -trimpath \
      -ldflags "-s -w -X github.com/youtonghy/TJXT_node/cmd.version=${version} -X github.com/youtonghy/TJXT_node/cmd.codename=tjxt-node" \
      -o "$out" .
  )
  mkdir -p "$PREFIX" /usr/local/bin
  install -m 755 "$out" "$(binary_path)"
  ln -sfn "$(binary_path)" "/usr/local/bin/${APP_NAME}"
  rm -f "$out"
  log "已安装 $(binary_path)"
}

write_systemd_unit() {
  local unit="/etc/systemd/system/${SERVICE_NAME}.service"
  cat >"$unit" <<EOF
[Unit]
Description=TJXT_node Xray agent for TONG-ladder
Documentation=https://github.com/youtonghy/TJXT_node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
WorkingDirectory=${PREFIX}
ExecStart=$(binary_path) server -c $(config_path)
Restart=always
RestartSec=5
LimitNOFILE=infinity
LimitNPROC=infinity
TasksMax=infinity
AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_ADMIN CAP_NET_RAW
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable "${SERVICE_NAME}.service"
  log "已安装 systemd 服务 ${SERVICE_NAME}"
}

write_openrc_unit() {
  local init="/etc/init.d/${SERVICE_NAME}"
  cat >"$init" <<EOF
#!/sbin/openrc-run
name="${SERVICE_NAME}"
command="$(binary_path)"
command_args="server -c $(config_path)"
command_background=true
pidfile="/run/${SERVICE_NAME}.pid"
EOF
  chmod 755 "$init"
  rc-update add "$SERVICE_NAME" default
  log "已安装 OpenRC 服务 ${SERVICE_NAME}"
}

install_ctl_wrapper() {
  local ctl="/usr/local/sbin/tjxt-node-deploy"
  install -m 755 "$SRC/deploy.sh" "$ctl"
  log "管理命令: ${ctl} start|stop|restart|status|log|update"
}

svc() {
  local action=$1
  if has_systemd; then
    systemctl "$action" "${SERVICE_NAME}"
  elif has_openrc; then
    rc-service "$SERVICE_NAME" "$action"
  else
    die "未检测到 systemd / OpenRC"
  fi
}

cmd_status() {
  if has_systemd; then
    systemctl status "${SERVICE_NAME}" --no-pager || true
  elif has_openrc; then
    rc-service "$SERVICE_NAME" status || true
  else
    die "未安装服务"
  fi
}

cmd_log() {
  if [[ -f "$(log_path)" ]]; then
    tail -n 100 -F "$(log_path)"
  elif has_systemd; then
    journalctl -u "${SERVICE_NAME}" -f --no-pager
  else
    die "找不到日志 $(log_path)"
  fi
}

cmd_uninstall() {
  if [[ "$YES" -ne 1 ]]; then
    is_tty || die "卸载请加 --yes"
    read -r -p "确认卸载 ${APP_NAME}? [y/N] " ans
    [[ "$ans" =~ ^[Yy]$ ]] || { log "已取消"; exit 0; }
  fi
  if has_systemd && systemctl list-unit-files "${SERVICE_NAME}.service" >/dev/null 2>&1; then
    systemctl disable --now "${SERVICE_NAME}" >/dev/null 2>&1 || true
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload || true
  fi
  if has_openrc && [[ -f "/etc/init.d/${SERVICE_NAME}" ]]; then
    rc-service "$SERVICE_NAME" stop >/dev/null 2>&1 || true
    rc-update del "$SERVICE_NAME" default >/dev/null 2>&1 || true
    rm -f "/etc/init.d/${SERVICE_NAME}"
  fi
  rm -f "$(binary_path)" "/usr/local/bin/${APP_NAME}" /usr/local/sbin/tjxt-node-deploy
  if [[ "$PURGE" -eq 1 ]]; then
    rm -rf "$CONFIG_DIR" "/var/log/${APP_NAME}" "$PREFIX"
    log "已删除配置和日志"
  else
    log "已卸载程序，配置保留在 $CONFIG_DIR"
  fi
}

install_service() {
  if has_systemd; then
    write_systemd_unit
    if [[ "$NO_START" -eq 0 ]]; then
      systemctl restart "${SERVICE_NAME}"
      log "服务已启动"
    fi
  elif has_openrc; then
    write_openrc_unit
    if [[ "$NO_START" -eq 0 ]]; then
      rc-service "$SERVICE_NAME" restart
      log "服务已启动"
    fi
  else
    warn "未检测到 systemd / OpenRC，请手动运行:"
    printf '  %s server -c %s\n' "$(binary_path)" "$(config_path)"
  fi
}

cmd_install() {
  need_root
  install_build_deps
  resolve_source "$@"
  ensure_go
  prompt_install_fields
  build_binary
  write_config
  install_ctl_wrapper
  install_service
  printf '\n'
  log "部署完成"
  printf '  二进制: %s\n' "$(binary_path)"
  printf '  配置:   %s\n' "$(config_path)"
  printf '  日志:   %s\n' "$(log_path)"
  printf '  管理:   tjxt-node-deploy status|log|restart|update\n'
  printf '  节点端口由面板下发，请自行在防火墙放行。\n'
}

cmd_update() {
  need_root
  install_build_deps
  resolve_source "$@"
  if [[ -z "$SRC_OVERRIDE" && "$SRC" == "$DEFAULT_SRC" ]]; then
    clone_or_update_src "$SRC"
  fi
  ensure_go
  build_binary
  install_ctl_wrapper
  if has_systemd || has_openrc; then
    if [[ "$NO_START" -eq 0 ]]; then
      svc restart
      log "已更新并重启"
    fi
  fi
}

cmd_local() {
  local dir
  dir="$(script_dir || true)"
  if [[ -n "$SRC_OVERRIDE" ]]; then
    SRC="$(cd "$SRC_OVERRIDE" && pwd)"
  elif [[ -n "$dir" && -f "$dir/go.mod" ]]; then
    SRC="$dir"
  else
    die "local 模式需要在仓库根目录运行，或指定 --source"
  fi
  command -v go >/dev/null 2>&1 || die "local 模式需要本机已安装 Go >= 1.26"
  go_version_ok || die "Go 版本过低，需要 >= 1.26（当前: $(go version)）"
  export CGO_ENABLED=0
  export GOEXPERIMENT=jsonv2
  export GOPROXY="${GOPROXY:-https://goproxy.cn,https://proxy.golang.org,direct}"
  mkdir -p "$SRC/build"
  log "本地编译到 $SRC/build/${APP_NAME}"
  (
    cd "$SRC"
    go build -trimpath -ldflags "-s -w" -o "build/${APP_NAME}" .
  )
  if [[ -n "$API_HOST" && -n "$NODE_ID" && -n "$API_KEY" ]]; then
    PREFIX="$SRC/build"
    CONFIG_DIR="$SRC/build"
    LOG_FILE="$SRC/build/node.log"
    FORCE_CONFIG=1
    write_config
    log "运行: $SRC/build/${APP_NAME} server -c $SRC/build/config.json"
  else
    log "运行: $SRC/build/${APP_NAME} server -c config.example.json"
  fi
}

show_menu() {
  while true; do
    printf '\n%sTJXT_node 管理%s\n' "$cyan" "$plain"
    printf '  1) 安装 / 更新\n'
    printf '  2) 启动\n'
    printf '  3) 停止\n'
    printf '  4) 重启\n'
    printf '  5) 状态\n'
    printf '  6) 日志\n'
    printf '  7) 卸载\n'
    printf '  0) 退出\n'
    read -r -p "选择: " n
    case "$n" in
      1) cmd_install ;;
      2) need_root; svc start ;;
      3) need_root; svc stop ;;
      4) need_root; svc restart ;;
      5) cmd_status ;;
      6) cmd_log ;;
      7) cmd_uninstall ;;
      0) exit 0 ;;
      *) warn "无效选择" ;;
    esac
  done
}

main() {
  parse_args "$@"
  if [[ -z "$CMD" ]]; then
    if is_tty; then
      if [[ -x "$(binary_path)" ]]; then
        CMD="menu"
      else
        CMD="install"
      fi
    else
      CMD="install"
    fi
  fi
  case "$CMD" in
    install) cmd_install "$@" ;;
    update)  cmd_update "$@" ;;
    local)   cmd_local ;;
    start)   need_root; svc start ;;
    stop)    need_root; svc stop ;;
    restart) need_root; svc restart ;;
    status)  cmd_status ;;
    log)     cmd_log ;;
    uninstall) cmd_uninstall ;;
    menu)    show_menu ;;
    *) die "未知命令: $CMD" ;;
  esac
}

main "$@"
