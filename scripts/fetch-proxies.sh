#!/usr/bin/env bash
# 从内置采集源抓代理，可选地当场验活，结果落到文件。
#
# 用法：
#   ./scripts/fetch-proxies.sh                          # 默认启用的源 → out/proxies-<时间>.txt
#   ./scripts/fetch-proxies.sh --all                    # 含默认关闭的源
#   ./scripts/fetch-proxies.sh --proto socks5 --check    # 只要 socks5，并验活
#   ./scripts/fetch-proxies.sh --family ipv6            # 只要 IPv6
#   ./scripts/fetch-proxies.sh --name geonode --check --concurrency 200
#   ./scripts/fetch-proxies.sh --out /tmp/p.txt
#
# --check 会逐个连出去实测，这一步慢且淘汰率高（免费 http 源通常只有
# 1%～5% 活着），所以默认不开。抓取和验活都复用面板里的同一套代码
# （cmd/fetchproxies、cmd/checkproxies），不是脚本里另写一份。

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

OUT=""
DO_CHECK=0
CHECK_PROTO=""
CONCURRENCY=100
CHECK_TIMEOUT="8s"
VALIDATE_URL="${FREE_VALIDATE_URL:-https://www.gstatic.com/generate_204}"
FETCH_ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --all)     FETCH_ARGS+=(-all); shift ;;
    --check)   DO_CHECK=1; shift ;;
    --proto)
      # 同一个值既用于筛源，也用于验活时的拨号方式。
      CHECK_PROTO="${2:?--proto 需要一个值}"
      FETCH_ARGS+=(-proto "${CHECK_PROTO}"); shift 2 ;;
    --format)  FETCH_ARGS+=(-format "${2:?--format 需要一个值}"); shift 2 ;;
    --name)    FETCH_ARGS+=(-name "${2:?--name 需要一个值}"); shift 2 ;;
    --family)  FETCH_ARGS+=(-family "${2:?--family 需要一个值}"); shift 2 ;;
    --out)     OUT="${2:?--out 需要一个值}"; shift 2 ;;
    --concurrency) CONCURRENCY="${2:?--concurrency 需要一个值}"; shift 2 ;;
    --timeout) CHECK_TIMEOUT="${2:?--timeout 需要一个值}"; shift 2 ;;
    --url)     VALIDATE_URL="${2:?--url 需要一个值}"; shift 2 ;;
    -h|--help) sed -n '2,17p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数：$1（用 --help 看用法）" >&2; exit 2 ;;
  esac
done

if [[ -z "${OUT}" ]]; then
  mkdir -p out
  OUT="out/proxies-$(date +%Y%m%d-%H%M%S).txt"
fi

RAW="${OUT}"
if [[ "${DO_CHECK}" == "1" ]]; then
  # 验活时先把原始结果写到临时文件，OUT 只留活的。
  RAW="$(mktemp)"
  trap 'rm -f "${RAW}"' EXIT
fi

echo "=== 抓取 ==="
go run ./cmd/fetchproxies "${FETCH_ARGS[@]}" -out "${RAW}"

RAW_COUNT="$(wc -l < "${RAW}" | tr -d ' ')"
if [[ "${RAW_COUNT}" == "0" ]]; then
  echo "没抓到任何代理，先跑 ./scripts/check-sources.sh 看源的状态" >&2
  exit 1
fi

if [[ "${DO_CHECK}" != "1" ]]; then
  echo
  echo "已写入 ${OUT}（${RAW_COUNT} 条，未验活）"
  echo "要验活：./scripts/fetch-proxies.sh --check ...，或"
  echo "  go run ./cmd/checkproxies -in ${OUT} -out live.txt"
  exit 0
fi

echo
echo "=== 验活 ==="
CHECK_ARGS=(-in "${RAW}" -out "${OUT}" -concurrency "${CONCURRENCY}"
            -timeout "${CHECK_TIMEOUT}" -url "${VALIDATE_URL}")
if [[ -n "${CHECK_PROTO}" ]]; then
  CHECK_ARGS+=(-proto "${CHECK_PROTO}")
fi
go run ./cmd/checkproxies "${CHECK_ARGS[@]}"

LIVE_COUNT="$(wc -l < "${OUT}" | tr -d ' ')"
echo
echo "已写入 ${OUT}（${LIVE_COUNT} 条存活 / ${RAW_COUNT} 条抓取）"
echo "导入面板：./scripts/import-proxies.sh --in ${OUT}"
