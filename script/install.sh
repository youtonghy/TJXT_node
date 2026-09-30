#!/usr/bin/env bash
# Compatibility wrapper. Use ../deploy.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
exec bash "$ROOT/deploy.sh" "$@"
