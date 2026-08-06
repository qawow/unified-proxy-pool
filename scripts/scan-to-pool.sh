#!/usr/bin/env bash
# 扫代理并直接写进池子（Redis），不需要面板在跑。
#
# 用法：
#   ./scripts/scan-to-pool.sh                        # 试跑：扫+验活，只报告不写
#   ./scripts/scan-to-pool.sh --write                # 真写进去
#   ./scripts/scan-to-pool.sh --in live.txt --write   # 从文件导入，不重新抓
#   ./scripts/scan-to-pool.sh --name b4rc0de-socks5 --write
#   ./scripts/scan-to-pool.sh --family ipv6 --write
#   ./scripts/scan-to-pool.sh --redis-db 15 --write   # 先拿一个空库试手
#   ./scripts/scan-to-pool.sh --skip-validate --limit 2000 --write
#
# 和 import-proxies.sh 的区别：
#   import-proxies.sh  走面板 HTTP 接口，一条一个请求，几百条以内合适
#   scan-to-pool.sh    直接调 store 的 AddRaw/MarkValidated，批量写，几万条也行
# 后者不需要面板在跑，但要能直连 Redis。
#
# 默认是**试跑**：只报告会发生什么，不动任何数据。加 --write 才落库。
# 写的是共享状态，所以这个开关是明确要求的，不是默认行为。
#
# 注意 MaxRawProxies = 4000：原始池到顶后 Trim 会按分数淘汰，而新抓的代理
# 分数都一样（ScoreInit），等于按平局随机踢。实测「写 563 条、池子只涨 52 条、
# 淘汰 511 条」——涨跌会在 raw 计数上互相抵消，光看总数看不出来，所以
# scanproxies 会把淘汰数单独报出来。一次别扫太多，或者先验活只写活的。

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

SCAN_ARGS=()
WRITE=0
REDIS_DB="${REDIS_DB:-0}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --write)         WRITE=1; shift ;;
    --in)            SCAN_ARGS+=(-in "${2:?--in 需要一个值}"); shift 2 ;;
    --name)          SCAN_ARGS+=(-name "${2:?--name 需要一个值}"); shift 2 ;;
    --all)           SCAN_ARGS+=(-all); shift ;;
    --family)        SCAN_ARGS+=(-family "${2:?--family 需要一个值}"); shift 2 ;;
    --proto)         SCAN_ARGS+=(-proto "${2:?--proto 需要一个值}"); shift 2 ;;
    --url)           SCAN_ARGS+=(-url "${2:?--url 需要一个值}"); shift 2 ;;
    --timeout)       SCAN_ARGS+=(-timeout "${2:?--timeout 需要一个值}"); shift 2 ;;
    --concurrency)   SCAN_ARGS+=(-concurrency "${2:?--concurrency 需要一个值}"); shift 2 ;;
    --limit)         SCAN_ARGS+=(-limit "${2:?--limit 需要一个值}"); shift 2 ;;
    --source)        SCAN_ARGS+=(-source "${2:?--source 需要一个值}"); shift 2 ;;
    --skip-validate) SCAN_ARGS+=(-skip-validate); shift ;;
    --redis)         SCAN_ARGS+=(-redis "${2:?--redis 需要一个值}"); shift 2 ;;
    --redis-db)      REDIS_DB="${2:?--redis-db 需要一个值}"; shift 2 ;;
    -h|--help)       sed -n '2,30p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数：$1（用 --help 看用法）" >&2; exit 2 ;;
  esac
done

SCAN_ARGS+=(-redis-db "${REDIS_DB}")

if [[ "${WRITE}" == "1" ]]; then
  echo "=== 扫描并写入 Redis DB ${REDIS_DB} ==="
  echo "这会修改共享数据。先确认库号对不对；想试手就用 --redis-db 15。"
  echo
  SCAN_ARGS+=(-write)
else
  echo "=== 试跑（不写任何数据）==="
  echo "确认结果没问题后加 --write 落库。"
  echo
fi

go run ./cmd/scanproxies "${SCAN_ARGS[@]}"

if [[ "${WRITE}" == "1" ]]; then
  echo
  echo "看池子状态："
  echo "  redis-cli -n ${REDIS_DB} SCARD upp:proxies:all      # 总数"
  echo "  redis-cli -n ${REDIS_DB} ZCARD upp:proxies:scored   # 已验证"
  echo "  curl -s http://127.0.0.1:7891/api/public/proxies/count   # 面板在跑的话"
fi
