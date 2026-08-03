#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PANEL_HOST="${PANEL_HOST:-0.0.0.0}"
PANEL_PORT="${PANEL_PORT:-7891}"
DATA_DIR="${DATA_DIR:-$ROOT_DIR/data}"
MIHOMO_BINARY="${MIHOMO_BINARY:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)
      PANEL_HOST="$2"
      shift 2
      ;;
    --port)
      PANEL_PORT="$2"
      shift 2
      ;;
    --data-dir)
      DATA_DIR="$2"
      shift 2
      ;;
    --mihomo-binary)
      MIHOMO_BINARY="$2"
      shift 2
      ;;
    *)
      echo "unknown option: $1" >&2
      exit 1
      ;;
  esac
done

export DATA_DIR PANEL_HOST PANEL_PORT
if [[ -n "$MIHOMO_BINARY" ]]; then
  export MIHOMO_BINARY
fi

cd "$ROOT_DIR"
echo "DATA_DIR=$DATA_DIR"
echo "PANEL_HOST=$PANEL_HOST"
echo "PANEL_PORT=$PANEL_PORT"
if [[ -n "${MIHOMO_BINARY:-}" ]]; then
  echo "MIHOMO_BINARY=$MIHOMO_BINARY"
else
  echo "MIHOMO_BINARY=<auto-detect>"
fi

go run ./cmd/app
