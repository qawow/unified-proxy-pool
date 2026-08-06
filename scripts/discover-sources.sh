#!/usr/bin/env bash
# 发现新的可用采集源：批量探测候选 URL，报告哪些值得加进 sources.go。
#
# 用法：
#   ./scripts/discover-sources.sh                       # 用内置候选清单，自动建基线
#   ./scripts/discover-sources.sh --in my-cands.txt     # 自己的候选清单
#   ./scripts/discover-sources.sh --emit-go             # 额外打印可直接粘贴的 Go 声明
#   ./scripts/discover-sources.sh --all                 # 连 DEAD/DUPLICATE 一起列出
#   ./scripts/discover-sources.sh --baseline b.txt      # 复用已有基线，跳过抓取
#   ./scripts/discover-sources.sh --no-baseline         # 不建基线（快，但查不出重复）
#
# 判定分三个问题，对应输出里的三列：
#   1. 能不能出货      → VERDICT 里的 DEAD / EMPTY / UNPARSED
#   2. 该用哪种 format → FORMAT 列（探测时逐个 format 试，取产出最多的）
#   3. 是不是新东西    → NEW / ADDS
#
# 第 3 点最容易漏。这些 GitHub 清单互相抄得很厉害：实测
# SoliSpirit/proxy-list 与 MuRongPIG/Proxy-Master，后者 101628 条地址
# 全部已包含在前者的 122777 条里。两个单独跟基线比都是「新增 7.4 万」，
# 但一起加进去只是把每轮抓取成本翻倍。所以：
#
#   NEW  = 相对基线的新增（每个候选单独算）
#   ADDS = 扣掉排名更高的候选已覆盖的部分之后，真正的增量
#
# 看 ADDS，不要看 NEW。镜像源的特征就是 NEW 很大而 ADDS 接近 0。
#
# 探测和解析都走 crawlers.NewDynamic，跟面板同一套代码，所以这里说能用的
# format，面板里就能用。

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

IN="scripts/source-candidates.txt"
BASELINE=""
BUILD_BASELINE=1
DISC_ARGS=()
KEEP_BASELINE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --in)          IN="${2:?--in 需要一个值}"; shift 2 ;;
    --baseline)    BASELINE="${2:?--baseline 需要一个值}"; BUILD_BASELINE=0; KEEP_BASELINE=1; shift 2 ;;
    --no-baseline) BUILD_BASELINE=0; shift ;;
    --emit-go)     DISC_ARGS+=(-emit-go); shift ;;
    --all)         DISC_ARGS+=(-all); shift ;;
    --min)         DISC_ARGS+=(-min "${2:?--min 需要一个值}"); shift 2 ;;
    --concurrency) DISC_ARGS+=(-concurrency "${2:?--concurrency 需要一个值}"); shift 2 ;;
    --timeout)     DISC_ARGS+=(-timeout "${2:?--timeout 需要一个值}"); shift 2 ;;
    -h|--help)     sed -n '2,30p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数：$1（用 --help 看用法）" >&2; exit 2 ;;
  esac
done

if [[ ! -r "${IN}" ]]; then
  echo "读不到候选清单：${IN}" >&2
  echo "格式见 scripts/source-candidates.txt（一行一个 URL，# 开头是注释）" >&2
  exit 1
fi

CAND_N="$(awk 'NF && $1 !~ /^#/' "${IN}" | wc -l | tr -d ' ')"
echo "候选清单 : ${IN}（${CAND_N} 条）"

if [[ "${BUILD_BASELINE}" == "1" ]]; then
  # 基线 = 当前启用的源实际能抓到的地址集合。没有它就判不出重复，
  # 而重复恰恰是加源时最容易踩的坑。
  BASELINE="$(mktemp)"
  trap 'rm -f "${BASELINE}"' EXIT
  echo "基线     : 现抓一份（跑一遍当前启用的源，几分钟）"
  echo
  go run ./cmd/fetchproxies -out "${BASELINE}" 2>&1 | tail -3
  echo
elif [[ -n "${BASELINE}" ]]; then
  if [[ ! -r "${BASELINE}" ]]; then
    echo "读不到基线文件：${BASELINE}" >&2
    exit 1
  fi
  echo "基线     : ${BASELINE}（$(wc -l < "${BASELINE}" | tr -d ' ') 条，复用）"
else
  echo "基线     : 无（--no-baseline，查不出重复源）"
fi

echo
echo "=== 探测 ==="
if [[ -n "${BASELINE}" ]]; then
  DISC_ARGS+=(-baseline "${BASELINE}")
fi
go run ./cmd/discover -in "${IN}" "${DISC_ARGS[@]}"

echo
echo "读数注意：基线只包含【默认启用】的源实际抓到的地址。"
echo "  所以镜像了某个「已配置但默认关闭」的源的候选，ADDS 会显示得很高。"
echo "  这个语义是对的（关掉的源不产出任何东西），但别据此以为它和现有源不重叠。"
echo "  想把关掉的源也算进基线：go run ./cmd/fetchproxies -all -out baseline.txt"
echo
echo "下一步："
echo "  1. 看 ADDS 列决定加哪些（ADDS 接近 0 的是镜像源，加了白费流量）"
echo "  2. 加 --emit-go 拿到可粘贴的 Go 声明，贴进 internal/crawlers/sources.go"
echo "  3. 产出量远超 MaxRawProxies（4000）的源，最后一个参数传 false 默认关掉——"
echo "     否则一轮就能填满原始池，Trim 会按分数淘汰掉其它源的代理"
echo "  4. go test ./internal/crawlers/ 会检查重名和重复 URL"
if [[ "${KEEP_BASELINE}" == "0" && "${BUILD_BASELINE}" == "1" ]]; then
  echo
  echo "提示：基线是临时文件，已删除。想复用就先存下来："
  echo "  go run ./cmd/fetchproxies -out baseline.txt"
  echo "  ./scripts/discover-sources.sh --baseline baseline.txt"
fi
