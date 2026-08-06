#!/usr/bin/env bash
# 检查内置采集源的健康状况：哪些能连、哪些空、哪些已经死了。
#
# 用法：
#   ./scripts/check-sources.sh                  # 只查默认启用的源
#   ./scripts/check-sources.sh --all            # 查全部（含默认关闭的）
#   ./scripts/check-sources.sh --proto socks5
#   ./scripts/check-sources.sh --name geonode,fatezero
#   ./scripts/check-sources.sh --stats          # 每个源的产出条数
#
# 源清单来自 `go run ./cmd/sources`，抓取走 `go run ./cmd/fetchproxies`，
# 都复用面板里跑的同一套 crawler 代码 —— 脚本里不再抄一份源列表，
# 否则清单会悄悄跟代码脱节。
#
# 输出里三种状态的区别很重要：
#   ok      —— 抓到并解析出了代理
#   SILENT  —— 请求成功、解析出 0 条：格式变了，或者解析器读不了这个源
#   FAILED  —— 连不上／404／超时
# SILENT 最值得追，因为它在面板里看起来和「源本来就没数据」一模一样。

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

FETCH_ARGS=()
STATS=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --all)     FETCH_ARGS+=(-all); shift ;;
    --stats)   STATS=1; shift ;;
    --proto)   FETCH_ARGS+=(-proto "${2:?--proto 需要一个值}"); shift 2 ;;
    --format)  FETCH_ARGS+=(-format "${2:?--format 需要一个值}"); shift 2 ;;
    --name)    FETCH_ARGS+=(-name "${2:?--name 需要一个值}"); shift 2 ;;
    --timeout) FETCH_ARGS+=(-timeout "${2:?--timeout 需要一个值}"); shift 2 ;;
    -h|--help) sed -n '2,25p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数：$1（用 --help 看用法）" >&2; exit 2 ;;
  esac
done

if [[ "${STATS}" == "1" ]]; then
  FETCH_ARGS+=(-stats)
fi

echo "=== 采集源健康检查 ==="
echo

# 代理地址本身不需要留下，这里只关心统计；所以丢到 /dev/null，
# 让 fetchproxies 的报告（写在 stderr）成为唯一输出。
go run ./cmd/fetchproxies "${FETCH_ARGS[@]}" -out /dev/null

echo
echo "提示："
echo "  SILENT 的源要看它的 format 对不对：go run ./cmd/sources --name <源名>"
echo "  单独细看一个源：go run ./cmd/fetchproxies -name <源名> -v"
