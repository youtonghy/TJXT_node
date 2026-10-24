#!/usr/bin/env bash
# TJXT_node one-click deploy for TONG-ladder.
# Downloads a release binary (or builds from source) and installs a systemd/openrc service.
set -euo pipefail

readonly APP_NAME="tjxt-node"
readonly DEFAULT_REPO="https://github.com/youtonghy/TJXT_node.git"
readonly DEFAULT_GITHUB_REPO="youtonghy/TJXT_node"
readonly DEFAULT_BRANCH="main"
readonly DEFAULT_GO_VERSION="1.27.1"
readonly DEFAULT_PREFIX="/usr/local/tjxt-node"
readonly DEFAULT_CONFIG_DIR="/etc/tjxt-node"
readonly DEFAULT_SRC="/usr/local/src/TJXT_node"
readonly DEFAULT_PROTOCOL="auto"
readonly SERVICE_NAME="tjxt-node"
readonly SERVICE_USER="tjxt-node"
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
GITHUB_REPO="${TJXT_GITHUB_REPO:-$DEFAULT_GITHUB_REPO}"
VERSION="${TJXT_VERSION:-latest}"
BUILD_FROM_SOURCE=0
FORCE_CONFIG=0
NO_START=0
YES=0
PURGE=0
SRC=""
PROMPTED=0
PAIR_INFO=""

log()  { printf "${green}[+]${plain} %s\n" "$*"; }
warn() { printf "${yellow}[!]${plain} %s\n" "$*" >&2; }
err()  { printf "${red}[x]${plain} %s\n" "$*" >&2; }
die()  { err "$*"; exit 1; }

usage() {
  cat <<'EOF'
TJXT_node 一键部署（TONG-ladder 节点）

用法:
  交互安装（依次询问面板地址和节点密钥）:
  curl -fsSL https://raw.githubusercontent.com/youtonghy/TJXT_node/main/deploy.sh | sudo bash

  非交互安装:
  sudo ./deploy.sh --api-host https://panel.example.com --api-key NODE_KEY
  curl -fsSL https://raw.githubusercontent.com/youtonghy/TJXT_node/main/deploy.sh \
    | sudo bash -s -- --api-host URL --api-key NODE_KEY

面板地址可以只写域名，或直接粘贴后台页面地址；脚本会补全 https://、去掉页面路径，
并向面板校验后使用可用的地址。

子命令:
  install      安装或更新（默认）
  update       更新二进制（默认下载最新 Release）
  start|stop|restart|status|log
  uninstall    卸载服务和二进制（默认保留配置）
  local        仅本地编译，不安装 systemd
  menu         交互菜单

选项:
  --api-host URL            面板地址，例如 https://panel.example.com
  --api-key KEY             节点自己的密钥（X-Server-Token），面板据此识别节点
  --node-id N               可选：面板节点 ID（旧的全局密钥需要它；给出时写入配置并校验）
  --protocol auto|v3ui|v1   对接协议，默认 auto
  --version TAG             Release 版本，默认 latest
  --build                   从源码编译，而不是下载 Release 二进制
  --branch NAME             源码分支（编译模式），默认 main
  --repo URL                Git 仓库地址
  --source DIR              使用已有源码目录编译
  --prefix DIR              安装目录，默认 /usr/local/tjxt-node
  --config-dir DIR          配置目录，默认 /etc/tjxt-node
  --force-config            覆盖已有配置（新配置先经 tjxt-node pair 校验）
  --no-start                安装后不启动服务
  --yes                     卸载时跳过确认
  --purge                   卸载时同时删除配置和日志
  -h, --help                显示帮助

环境变量: TJXT_API_HOST TJXT_NODE_ID TJXT_API_KEY TJXT_PROTOCOL TJXT_VERSION GOPROXY
私有仓库: 导出 GITHUB_TOKEN（需 repo 读权限）后通过 GitHub API 下载 Release。
镜像: TJXT_RELEASE_BASE_URL 指向提供相同文件名的下载地址（含 SHA256SUMS）。
国内编译可设: GOPROXY=https://goproxy.cn,direct

本脚本不会关闭防火墙、不会上报统计。节点监听端口由面板下发，请自行放行。
EOF
}

is_tty() { [[ -t 0 && -t 1 ]]; }

# can_prompt is true when a terminal can answer questions, also under
# curl ... | sudo bash where stdin is the script itself.
can_prompt() { { : </dev/tty; } 2>/dev/null; }

# ask VAR PROMPT [-s] reads one line from the terminal into VAR; -s hides it.
# End of input (Ctrl-D) cancels instead of repeating the question forever.
ask() {
  local __ans="" __rc=0
  if [[ "${3:-}" == "-s" ]]; then
    IFS= read -r -s -p "$2" __ans </dev/tty || __rc=$?
    printf '\n' >&2
  else
    IFS= read -r -p "$2" __ans </dev/tty || __rc=$?
  fi
  ((__rc == 0)) || [[ -n "$__ans" ]] || die "输入已结束，已取消"
  printf -v "$1" '%s' "$__ans"
}

# mask_key keeps at most the last four characters of a key, like tjxt-node pair.
mask_key() {
  local k=$1
  if ((${#k} < 12)); then printf '****'; else printf '****%s' "${k: -4}"; fi
}

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
      --version)
        VERSION="${2:-}"; shift 2 ;;
      --version=*)
        VERSION="${1#*=}"; shift ;;
      --build)
        BUILD_FROM_SOURCE=1; shift ;;
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
  if ((${#missing[@]})); then
    die "缺少命令: ${missing[*]}"
  fi
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
  version_ge "$raw" "1.27"
}

ensure_go() {
  if go_version_ok; then
    log "使用已有 Go: $(go version)"
    return
  fi
  local os arch tarball tmp dest
  os="$(os_id)"
  [[ "$os" == "darwin" || "$os" == "linux" ]] || die "请先安装 Go >= 1.27"
  arch="$(go_arch)"
  tarball="go${DEFAULT_GO_VERSION}.${os}-${arch}.tar.gz"
  dest="/usr/local/go"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"; trap - RETURN' RETURN
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

# normalize_api_host turns what a user pastes (panel.example.com,
# https://panel.example.com/#/admin, https://panel.example.com/api/v1/...) into
# the http(s)://host[:port][/path] form tjxt-node expects: whitespace, quotes,
# query, fragment, API paths and trailing slashes are dropped and https:// is
# added when no scheme is given. It fails on other schemes or a bad host.
normalize_api_host() {
  local s=$1 scheme rest hostport path=""
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  s="${s#[\"\']}"; s="${s%[\"\']}"
  s="${s%%#*}"; s="${s%%\?*}"
  [[ "$s" =~ [[:space:]] ]] && return 1
  if [[ "$s" =~ ^([A-Za-z][A-Za-z0-9+.-]*)://(.*)$ ]]; then
    scheme="$(tr '[:upper:]' '[:lower:]' <<<"${BASH_REMATCH[1]}")"
    rest=${BASH_REMATCH[2]}
  else
    scheme=https
    rest=$s
  fi
  [[ "$scheme" == http || "$scheme" == https ]] || return 1
  hostport=${rest%%/*}
  [[ "$rest" == */* ]] && path="/${rest#*/}"
  [[ "$hostport" =~ ^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$ ]] || return 1
  hostport="$(tr '[:upper:]' '[:lower:]' <<<"$hostport")"
  [[ "$path" == /api || "$path" == /api/* ]] && path=""
  path="${path%%/api/*}"
  while [[ "$path" == */ ]]; do path=${path%/}; done
  printf '%s://%s%s' "$scheme" "$hostport" "$path"
}

# api_host_candidates prints the addresses to try, best first: the normalized
# address, the same without its path (a pasted admin page), and over http when
# the user gave no scheme.
api_host_candidates() {
  local raw=$1 first origin c
  first="$(normalize_api_host "$raw")" || return 1
  origin="$(sed -E 's#^(https?://[^/]+).*#\1#' <<<"$first")"
  local -a list=("$first")
  [[ "$origin" != "$first" ]] && list+=("$origin")
  if [[ ! "${raw//[[:space:]]/}" =~ ^[\"\']?[A-Za-z][A-Za-z0-9+.-]*:// ]]; then
    for c in "${list[@]}"; do list+=("http://${c#https://}"); done
  fi
  printf '%s\n' "${list[@]}"
}

# ask_api_host asks until the answer looks like a panel address.
ask_api_host() {
  local ans prev=$API_HOST
  while true; do
    if [[ -n "$prev" ]]; then
      ask ans "面板地址（回车沿用 ${prev}）: "
      [[ -z "$ans" ]] && ans=$prev
    else
      ask ans "面板地址（例如 panel.example.com 或 https://panel.example.com）: "
    fi
    if [[ -z "${ans//[[:space:]]/}" ]]; then
      warn "面板地址不能为空"
    elif normalize_api_host "$ans" >/dev/null; then
      API_HOST=$ans
      return
    else
      warn "无法识别的面板地址: ${ans}（只支持 http/https 域名或 IP）"
    fi
  done
}

ask_api_key() {
  local ans
  while true; do
    ask ans "节点密钥（面板节点列表中该节点的密钥，输入不显示）: " -s
    ans="${ans//[[:space:]]/}"
    if [[ -n "$ans" ]]; then
      API_KEY=$ans
      return
    fi
    warn "节点密钥不能为空"
  done
}

# prompt_install_fields asks for what the command line did not give. Under
# curl | bash it reads from the terminal.
prompt_install_fields() {
  validate_node_id
  if [[ -n "$API_HOST" && -n "$API_KEY" ]]; then
    return
  fi
  local cfg ans
  cfg="$(config_path)"
  if [[ -f "$cfg" && "$FORCE_CONFIG" -eq 0 ]]; then
    if ! can_prompt; then
      log "已有配置 $cfg，跳过填写（需要覆盖请加 --force-config）"
      return
    fi
    ask ans "检测到已有配置 ${cfg}，是否重新填写面板地址和密钥？[y/N] "
    if [[ ! "$ans" =~ ^[Yy] ]]; then
      log "保留已有配置"
      return
    fi
    FORCE_CONFIG=1
  fi
  can_prompt || die "缺少 --api-host --api-key。非交互安装必须提供这两项。"
  PROMPTED=1
  printf "${cyan}对接 TONG-ladder 面板${plain}\n"
  [[ -z "$API_HOST" ]] && ask_api_host
  [[ -z "$API_KEY" ]] && ask_api_key
  return 0
}

needs_config() { [[ ! -f "$(config_path)" || "$FORCE_CONFIG" -eq 1 ]]; }

# verify_panel checks API_HOST and API_KEY with tjxt-node pair --dry-run,
# trying each candidate address. On success API_HOST becomes the address that
# worked and PAIR_INFO holds pair's report. Returns pair's exit code.
verify_panel() {
  local bin cand out rc=2
  bin="$(binary_path)"
  if [[ ! -x "$bin" ]]; then
    err "找不到 ${bin}，无法向面板校验"
    return 1
  fi
  local -a cands=()
  while IFS= read -r cand; do
    cands+=("$cand")
  done < <(api_host_candidates "$API_HOST" || true)
  if ((${#cands[@]} == 0)); then
    err "面板地址格式不正确: ${API_HOST}（示例: https://panel.example.com）"
    return 2
  fi
  for cand in "${cands[@]}"; do
    log "向面板校验: ${cand}"
    local -a args=(pair --dry-run --api-host "$cand" --protocol "$PROTOCOL")
    [[ -n "$NODE_ID" ]] && args+=(--node-id "$NODE_ID")
    rc=0
    out="$(TJXT_API_KEY="$API_KEY" "$bin" "${args[@]}" 2>&1)" || rc=$?
    if ((rc == 0)); then
      API_HOST=$cand
      PAIR_INFO=$out
      return 0
    fi
    warn "$(grep '^tjxt-node:' <<<"$out" || printf '%s' "$out")"
    # The panel answered, so the address is right: the key or the node is wrong.
    if [[ "$rc" -eq 4 || "$rc" -eq 5 || "$rc" -eq 8 ]]; then
      API_HOST=$cand
      break
    fi
  done
  case "$rc" in
    3) err "无法连接面板，请检查地址、端口、DNS 和本机网络" ;;
    4) err "面板拒绝了密钥：请在面板节点列表中复制该节点的密钥（可能已重置）" ;;
    5) err "这是全局通讯密钥，不是节点自己的密钥；请改用面板节点列表中该节点的密钥" ;;
    7) err "该地址不是面板的节点接口：请确认地址是面板地址（不是订阅或节点地址）" ;;
    8) err "面板已响应，但该节点的配置无法使用（原因见上）：请升级 tjxt-node，或检查面板中该节点的设置" ;;
    *) err "面板校验失败（原因见上）" ;;
  esac
  return "$rc"
}

# show_pair_info prints what the panel reported for the key.
show_pair_info() {
  local id type name addr
  id="$(sed -nE 's/^Key belongs to node ([0-9]+).*/\1/p' <<<"$PAIR_INFO")"
  type="$(sed -nE 's/^Key belongs to node [0-9]+ \(type ([^,)]*).*/\1/p' <<<"$PAIR_INFO")"
  name="$(sed -nE 's/^Key belongs to node .*, name "(.*)"\)$/\1/p' <<<"$PAIR_INFO")"
  addr="$(sed -nE 's/^Node (address|port) (.*)$/\2/p' <<<"$PAIR_INFO")"
  printf "\n${green}面板校验通过，获取到的节点信息:${plain}\n"
  printf '  面板地址: %s\n' "$API_HOST"
  printf '  节点密钥: %s\n' "$(mask_key "$API_KEY")"
  if [[ -n "$id" ]]; then
    printf '  节点 ID:  %s\n' "$id"
    printf '  节点类型: %s\n' "${type:-未知}"
    [[ -n "$name" ]] && printf '  节点名称: %s\n' "$name"
    [[ -n "$addr" ]] && printf '  节点地址: %s\n' "$addr"
  else
    printf '%s\n' "$PAIR_INFO" | sed 's/^/  /'
  fi
  printf '  对接协议: %s\n\n' "$PROTOCOL"
}

# confirm_panel verifies host and key before the config is written. When the
# values were typed in, a failure asks again and the user confirms the node.
confirm_panel() {
  needs_config || return 0
  [[ -n "$API_HOST" && -n "$API_KEY" ]] || die "写配置需要 --api-host --api-key"
  local rc ans
  while true; do
    rc=0
    verify_panel || rc=$?
    if ((rc == 0)); then
      show_pair_info
      ((PROMPTED)) || return 0
      ask ans "确认使用该节点并完成安装？[Y/n] "
      [[ "$ans" =~ ^[Nn] ]] || return 0
    elif ((!PROMPTED)); then
      die "面板校验失败（原因见上），未写入配置，服务未启动"
    fi
    ask ans "重新填写面板地址和密钥？[Y/n] "
    [[ "$ans" =~ ^[Nn] ]] && die "已取消，未写入配置，服务未启动"
    ask_api_host
    case "$rc" in
      3 | 7) ;; # unreachable or not a panel: the key was never checked
      *) ask_api_key ;;
    esac
  done
}

validate_node_id() {
  [[ -z "$NODE_ID" || "$NODE_ID" =~ ^[0-9]+$ ]] || die "NodeID 必须是数字"
}

# write_config writes the config for a fresh install or --force-config. The
# new config goes to a temporary file that tjxt-node pair verifies against the
# panel (and fills with the node id the key belongs to); only then does it
# replace the real config, so a wrong host or key leaves the old one and the
# service untouched. An existing config is kept as is.
write_config() {
  local cfg dest_dir
  cfg="$(config_path)"
  dest_dir="$(dirname "$cfg")"
  mkdir -p "$dest_dir" "$(dirname "$(log_path)")"
  if [[ -f "$cfg" && "$FORCE_CONFIG" -eq 0 ]]; then
    log "保留已有配置 $cfg"
    return
  fi
  [[ -n "$API_HOST" && -n "$API_KEY" ]] || die "写配置需要 --api-host --api-key"
  validate_node_id
  local host key proto logf node_id_line="" tmp
  host="$(json_escape "$API_HOST")"
  key="$(json_escape "$API_KEY")"
  proto="$(json_escape "$PROTOCOL")"
  logf="$(json_escape "$(log_path)")"
  [[ -n "$NODE_ID" ]] && node_id_line="\"NodeID\": ${NODE_ID},"
  tmp="${cfg}.pairing"
  umask 077
  cat >"$tmp" <<EOF
{
  "Log": {
    "Level": "info",
    "Output": "${logf}",
    "Access": "none"
  },
  "Nodes": [
    {
      "ApiHost": "${host}",
      ${node_id_line}
      "ApiKey": "${key}",
      "Protocol": "${proto}",
      "Timeout": 15
    }
  ]
}
EOF
  chmod 600 "$tmp"
  if ! pair_config "$tmp"; then
    rm -f "$tmp"
    die "面板校验失败（原因见上），未写入配置，服务未启动"
  fi
  mv -f "$tmp" "$cfg"
  log "写入配置 $cfg"
}

# pair_config FILE checks host and key with the panel and rewrites FILE's node
# with the node id the panel reports, keeping its Log settings. The key goes
# through the environment so it does not appear in the process list.
pair_config() {
  local file=$1 bin
  bin="$(binary_path)"
  if [[ ! -x "$bin" ]]; then
    err "找不到 ${bin}，无法向面板校验"
    return 1
  fi
  local -a args=(pair --api-host "$API_HOST" --protocol "$PROTOCOL" -c "$file")
  [[ -n "$NODE_ID" ]] && args+=(--node-id "$NODE_ID")
  log "写入节点配置"
  TJXT_API_KEY="$API_KEY" "$bin" "${args[@]}" >/dev/null
}

# Source builds are used with --build/--source or when deploy.sh runs from a
# checkout; otherwise the release binary is downloaded.
wants_source_build() {
  [[ "$BUILD_FROM_SOURCE" -eq 1 || -n "$SRC_OVERRIDE" ]] && return 0
  local dir
  dir="$(script_dir || true)"
  [[ -n "$dir" && -f "$dir/go.mod" ]]
}

install_download_deps() {
  # nftables lets Hysteria 2 nodes redirect their port hopping range (optional).
  local pkgs=(curl ca-certificates nftables)
  [[ -n "${GITHUB_TOKEN:-}" ]] && pkgs+=(jq)
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -y
    DEBIAN_FRONTEND=noninteractive apt-get install -y "${pkgs[@]}"
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y "${pkgs[@]}"
  elif command -v yum >/dev/null 2>&1; then
    yum install -y "${pkgs[@]}"
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache "${pkgs[@]}"
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Sy --noconfirm "${pkgs[@]}"
  fi
  ensure_commands curl sha256sum
  [[ -n "${GITHUB_TOKEN:-}" ]] && ensure_commands jq
  return 0
}

# fetch_release_assets DIR NAME... downloads release assets into DIR. Without
# GITHUB_TOKEN it uses the public download URLs; with it, the GitHub API
# (required while the repository is private).
fetch_release_assets() {
  local dir=$1 name
  shift
  if [[ -z "${GITHUB_TOKEN:-}" ]]; then
    local base="https://github.com/${GITHUB_REPO}/releases/latest/download"
    [[ "$VERSION" != "latest" ]] && base="https://github.com/${GITHUB_REPO}/releases/download/${VERSION}"
    # A mirror serving the same asset names can replace GitHub.
    [[ -n "${TJXT_RELEASE_BASE_URL:-}" ]] && base="${TJXT_RELEASE_BASE_URL%/}"
    for name in "$@"; do
      log "下载 ${base}/${name}"
      curl -fL --retry 3 --retry-delay 2 -o "${dir}/${name}" "${base}/${name}" \
        || die "下载 ${name} 失败（仓库私有时请导出 GITHUB_TOKEN，或用 --build 从源码编译）"
    done
    return
  fi
  local api="https://api.github.com/repos/${GITHUB_REPO}/releases/latest" release url
  [[ "$VERSION" != "latest" ]] && api="https://api.github.com/repos/${GITHUB_REPO}/releases/tags/${VERSION}"
  release="$(curl -fsSL --retry 3 -H "Authorization: Bearer ${GITHUB_TOKEN}" \
    -H "Accept: application/vnd.github+json" "$api")" || die "查询 Release 失败: $api"
  for name in "$@"; do
    url="$(jq -r --arg n "$name" '.assets[] | select(.name == $n) | .url' <<<"$release")"
    [[ -n "$url" && "$url" != "null" ]] || die "Release 中没有 ${name}"
    log "下载 ${name}（$(jq -r '.tag_name' <<<"$release")）"
    curl -fL --retry 3 --retry-delay 2 -H "Authorization: Bearer ${GITHUB_TOKEN}" \
      -H "Accept: application/octet-stream" -o "${dir}/${name}" "$url" || die "下载 ${name} 失败"
  done
}

# verify_sha256 DIR NAME requires NAME to be listed in DIR/SHA256SUMS and to match.
verify_sha256() {
  local dir=$1 name=$2 want got
  want="$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1; exit }' "${dir}/SHA256SUMS")"
  [[ -n "$want" ]] || die "SHA256SUMS 中没有 ${name}，已中止安装"
  got="$(sha256sum "${dir}/${name}" | awk '{ print $1 }')"
  [[ "$got" == "$want" ]] || die "${name} SHA256 校验失败，已中止安装"
}

# download_release installs the release binary and deploy.sh after checking
# both against SHA256SUMS.
download_release() {
  local asset tmp
  asset="${APP_NAME}-linux-$(go_arch)"
  [[ "$(os_id)" == "linux" ]] || die "Release 只提供 Linux 二进制，其他系统请用 --build"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"; trap - RETURN' RETURN
  fetch_release_assets "$tmp" SHA256SUMS "$asset" deploy.sh
  [[ -n "${TJXT_RELEASE_BASE_URL:-}" ]] && warn "SHA256SUMS 来自镜像本身，只能发现传输损坏，无法证明文件来源"
  verify_sha256 "$tmp" "$asset"
  verify_sha256 "$tmp" deploy.sh
  mkdir -p "$PREFIX" /usr/local/bin
  install -m 755 "${tmp}/${asset}" "$(binary_path)"
  ln -sfn "$(binary_path)" "/usr/local/bin/${APP_NAME}"
  install -m 755 "${tmp}/deploy.sh" "${PREFIX}/deploy.sh"
  log "已安装 $(binary_path)（$("$(binary_path)" version 2>/dev/null | head -n 1 || echo unknown)）"
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

# The service runs as an unprivileged system user with only the capabilities it
# needs (ports below 1024, TUN). Config, certs and logs belong to that user.
ensure_service_user() {
  if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1; then
      useradd --system --no-create-home --home-dir "$PREFIX" --shell /usr/sbin/nologin "$SERVICE_USER"
    elif command -v adduser >/dev/null 2>&1; then
      addgroup -S "$SERVICE_USER" 2>/dev/null || true
      adduser -S -D -H -h "$PREFIX" -s /sbin/nologin -G "$SERVICE_USER" "$SERVICE_USER"
    else
      die "无法创建系统用户 ${SERVICE_USER}（缺少 useradd/adduser）"
    fi
    log "已创建系统用户 ${SERVICE_USER}"
  fi
  own_service_dir "$CONFIG_DIR"
  own_service_dir "$(dirname "$(log_path)")"
  [[ -f "$(config_path)" ]] && chmod 600 "$(config_path)"
  return 0
}

# own_service_dir hands a directory to the service user. It refuses directories
# that are not dedicated to tjxt-node, so --config-dir /etc or TJXT_LOG_FILE in
# /var/log cannot re-own a system directory.
own_service_dir() {
  local dir=$1
  case "$(basename "$dir")" in
    *tjxt*) ;;
    *) die "${dir} 不是 tjxt-node 专用目录（目录名需包含 tjxt），拒绝修改属主" ;;
  esac
  mkdir -p "$dir"
  chown -R "${SERVICE_USER}:${SERVICE_USER}" "$dir"
  chmod 750 "$dir"
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
User=${SERVICE_USER}
Group=${SERVICE_USER}
WorkingDirectory=${PREFIX}
ExecStart=$(binary_path) server -c $(config_path)
Restart=always
RestartSec=5
LimitNOFILE=infinity
LimitNPROC=infinity
TasksMax=infinity
AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_ADMIN
NoNewPrivileges=true
ProtectSystem=full
ReadWritePaths=${CONFIG_DIR} -$(dirname "$(log_path)")
ProtectHome=true
PrivateTmp=true

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
command_user="${SERVICE_USER}:${SERVICE_USER}"
capabilities="^cap_net_bind_service,^cap_net_admin"
pidfile="/run/${SERVICE_NAME}.pid"
EOF
  chmod 755 "$init"
  rc-update add "$SERVICE_NAME" default
  log "已安装 OpenRC 服务 ${SERVICE_NAME}"
}

install_ctl_wrapper() {
  local ctl="/usr/local/sbin/tjxt-node-deploy"
  local script="${PREFIX}/deploy.sh"
  [[ -n "$SRC" ]] && script="$SRC/deploy.sh"
  install -m 755 "$script" "$ctl"
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
    can_prompt || die "卸载请加 --yes"
    local ans
    ask ans "确认卸载 ${APP_NAME}? [y/N] "
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
  ensure_service_user
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

# service_state prints whether the service starts at boot and is running.
service_state() {
  local enabled="未开启" active="未运行"
  if has_systemd; then
    systemctl is-enabled --quiet "${SERVICE_NAME}" 2>/dev/null && enabled="已开启"
    systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null && active="运行中"
  elif has_openrc; then
    rc-update show default 2>/dev/null | grep -qw "${SERVICE_NAME}" && enabled="已开启"
    rc-service "$SERVICE_NAME" status >/dev/null 2>&1 && active="运行中"
  else
    enabled="未安装服务"
  fi
  [[ "$NO_START" -eq 1 && "$active" != "运行中" ]] && active="未启动（--no-start）"
  printf '  开机自启: %s\n' "$enabled"
  printf '  运行状态: %s\n' "$active"
  [[ "$active" == "未运行" ]] && printf '  服务未在运行，请查看日志: tjxt-node-deploy log\n'
  return 0
}

cmd_install() {
  need_root
  if wants_source_build; then
    install_build_deps
    resolve_source "$@"
    ensure_go
    prompt_install_fields
    build_binary
  else
    install_download_deps
    prompt_install_fields
    download_release
  fi
  confirm_panel
  write_config
  install_ctl_wrapper
  install_service
  # Give a failing start a moment to show up in the state below.
  if [[ "$NO_START" -eq 0 ]] && { has_systemd || has_openrc; }; then
    sleep 3
  fi
  printf '\n'
  log "部署完成"
  printf '  二进制:   %s\n' "$(binary_path)"
  printf '  配置:     %s\n' "$(config_path)"
  printf '  日志:     %s\n' "$(log_path)"
  service_state
  printf '  管理:     tjxt-node-deploy status|log|restart|update\n'
  printf '  节点端口由面板下发，请自行在防火墙放行。\n'
}

cmd_update() {
  need_root
  if wants_source_build; then
    install_build_deps
    resolve_source "$@"
    if [[ -z "$SRC_OVERRIDE" && "$SRC" == "$DEFAULT_SRC" ]]; then
      clone_or_update_src "$SRC"
    fi
    ensure_go
    build_binary
  else
    install_download_deps
    download_release
  fi
  install_ctl_wrapper
  # Rewrite the service unit so older root installs move to the service user.
  install_service
  log "已更新"
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
  command -v go >/dev/null 2>&1 || die "local 模式需要本机已安装 Go >= 1.27"
  go_version_ok || die "Go 版本过低，需要 >= 1.27（当前: $(go version)）"
  export CGO_ENABLED=0
  export GOEXPERIMENT=jsonv2
  export GOPROXY="${GOPROXY:-https://goproxy.cn,https://proxy.golang.org,direct}"
  mkdir -p "$SRC/build"
  log "本地编译到 $SRC/build/${APP_NAME}"
  (
    cd "$SRC"
    go build -trimpath -ldflags "-s -w" -o "build/${APP_NAME}" .
  )
  if [[ -n "$API_HOST" && -n "$API_KEY" ]]; then
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
    printf "\n${cyan}TJXT_node 管理${plain}\n"
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
