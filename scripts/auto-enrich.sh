#!/usr/bin/env bash
# 自动打野：定时抓取 → 验活 → 只把活的写进池子，并按周期复查源质量。
#
# 用法：
#   ./scripts/auto-enrich.sh                     # 试跑一轮，不写数据
#   ./scripts/auto-enrich.sh --write             # 跑一轮并落库
#   ./scripts/auto-enrich.sh --write --yield     # 额外测一遍各源产出
#   ./scripts/auto-enrich.sh --write --discover  # 额外探测候选源
#   ./scripts/auto-enrich.sh --write --yield --persist   # 把测量记进历史
#   ./scripts/auto-enrich.sh --tune              # 只打印开关建议（读历史）
#   ./scripts/auto-enrich.sh --write --tune-apply # 真按历史调开关
#   ./scripts/auto-enrich.sh --write --redis-db 15
#
# 挂 cron（每 30 分钟补一轮，每天凌晨 4 点复查源质量并调开关）：
#   */30 * * * * cd /path/to/unified-proxy-pool && ./scripts/auto-enrich.sh --write >> logs/auto-enrich.log 2>&1
#   0 4 * * *    cd /path/to/unified-proxy-pool && ./scripts/auto-enrich.sh --write --yield --persist --tune-apply --discover >> logs/auto-enrich.log 2>&1
#
# 四个设计取舍，都是被实测逼出来的：
#
# 1. 先验活再写，不写原始池。免费 http 源存活率通常 1%～5%，把几万条原始
#    地址灌进去只会撑爆 MaxRawProxies（4000）；实测「写 563 条新地址、
#    池子只涨 52 条、Trim 淘汰 511 条」，淘汰的还可能是其它源的代理。
#    所以这里 --check 之后只写活的，用 --raw 才走原始池。
#
# 2. 带锁。一轮抓取+验活可能十几分钟，cron 每 30 分钟拉一次就会重叠，
#    两个进程同时往 Redis 写会互相触发 Trim。
#
# 3. 单次测量不下结论。--yield 只打印建议；要自动调开关得 --tune-apply，
#    而它读的是 --persist 攒下来的历史：某个源要连续几轮都读作死才会被关。
#    一次网络抖动不该让某个源被永久关掉。
#
# 4. 多数源同时读作死时 sourcetune 直接拒绝执行（退出码 3）。源不会一起坏，
#    这个形状说明是校验 URL 被墙或本机断网 —— 照着关会把整个池子的输入关光，
#    而开回来是手工活。

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

WRITE=0
DO_YIELD=0
DO_DISCOVER=0
PERSIST_YIELD=0
DO_TUNE=0
TUNE_APPLY=0
RAW_MODE=0
REDIS_DB="${REDIS_DB:-0}"
# 默认扫描量按原始池上限留出余量：验活后活的通常只剩几个百分点，
# 一轮补几百条活代理，既不会撑爆池子也够用。
LIMIT=3000
CONCURRENCY=150
TIMEOUT="8s"
SOURCE_NAMES=""
FAMILY=""
LOCK_FILE="${TMPDIR:-/tmp}/upp-auto-enrich.lock"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --write)       WRITE=1; shift ;;
    --yield)       DO_YIELD=1; shift ;;
    --discover)    DO_DISCOVER=1; shift ;;
    --persist)     PERSIST_YIELD=1; shift ;;
    --tune)        DO_TUNE=1; shift ;;
    --tune-apply)  DO_TUNE=1; TUNE_APPLY=1; shift ;;
    --raw)         RAW_MODE=1; shift ;;
    --name)        SOURCE_NAMES="${2:?--name 需要一个值}"; shift 2 ;;
    --family)      FAMILY="${2:?--family 需要一个值}"; shift 2 ;;
    --limit)       LIMIT="${2:?--limit 需要一个值}"; shift 2 ;;
    --concurrency) CONCURRENCY="${2:?--concurrency 需要一个值}"; shift 2 ;;
    --timeout)     TIMEOUT="${2:?--timeout 需要一个值}"; shift 2 ;;
    --redis-db)    REDIS_DB="${2:?--redis-db 需要一个值}"; shift 2 ;;
    --lock)        LOCK_FILE="${2:?--lock 需要一个值}"; shift 2 ;;
    # Print the header block by matching it, not by line number: a hardcoded
    # range silently truncates help the next time the header grows, which is
    # exactly what happened when --tune was documented.
    -h|--help)     awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数：$1（用 --help 看用法）" >&2; exit 2 ;;
  esac
done

# ---- 锁：cron 重叠时直接退出，不排队 ----
# 用 flock 的 -n（非阻塞）：排队会让积压的任务在网络恢复后一起涌出去。
exec 9>"${LOCK_FILE}"
if ! flock -n 9; then
  echo "[$(date '+%F %T')] 上一轮还在跑（锁 ${LOCK_FILE}），本轮跳过"
  exit 0
fi

started="$(date '+%F %T')"
echo "=========================================================="
echo "[${started}] auto-enrich 开始  write=${WRITE} db=${REDIS_DB} limit=${LIMIT}"
echo "=========================================================="

# ---- 第一步：可选，探测候选源 ----
if [[ "${DO_DISCOVER}" == "1" ]]; then
  echo
  echo "--- 探测候选源 ---"
  # 不建基线：建基线要先跑一遍全部启用源，太慢，不适合放在定时任务里。
  # 这里只回答「还活着吗、该用哪个 format」，重复判定留给人工跑
  # ./scripts/discover-sources.sh。
  if ! go run ./cmd/discover -in scripts/source-candidates.txt -min 50 2>&1 | tail -25; then
    echo "探测失败，继续后面的步骤"
  fi
fi

# ---- 第二步：抓取 + 验活 + 入池 ----
echo
echo "--- 抓取并补池 ---"
SCAN_ARGS=(-limit "${LIMIT}" -concurrency "${CONCURRENCY}" -timeout "${TIMEOUT}"
           -redis-db "${REDIS_DB}")
[[ -n "${SOURCE_NAMES}" ]] && SCAN_ARGS+=(-name "${SOURCE_NAMES}")
[[ -n "${FAMILY}" ]] && SCAN_ARGS+=(-family "${FAMILY}")
if [[ "${RAW_MODE}" == "1" ]]; then
  # 不验活直接写原始池，交给面板的校验轮去打分。快，但会占满 raw 配额。
  SCAN_ARGS+=(-skip-validate)
fi
[[ "${WRITE}" == "1" ]] && SCAN_ARGS+=(-write)

scan_ok=1
go run ./cmd/scanproxies "${SCAN_ARGS[@]}" || scan_ok=0
if [[ "${scan_ok}" == "0" ]]; then
  echo "补池这一步失败了（见上面的错误）"
fi

# ---- 第三步：可选，复查各源产出 ----
if [[ "${DO_YIELD}" == "1" ]]; then
  echo
  echo "--- 复查各源产出（抽样实测，慢）---"
  # -emit-toggles 只打印命令不执行：见文件头第 3 条。
  YIELD_ARGS=(-sample 80 -timeout "${TIMEOUT}" -emit-toggles)
  [[ "${PERSIST_YIELD}" == "1" ]] && YIELD_ARGS+=(-persist -redis-db "${REDIS_DB}")
  go run ./cmd/sourceyield "${YIELD_ARGS[@]}" 2>&1 | tail -60 \
    || echo "源质量复查失败，不影响已完成的补池"
fi

# ---- 第四步：可选，按历史记录调整源开关 ----
if [[ "${DO_TUNE}" == "1" ]]; then
  echo
  echo "--- 按历史产出调整源开关 ---"
  # 编译成二进制再跑，不用 go run：go run 把子进程的退出码一律压成 1，
  # 于是「拒绝执行（3）」和「自己崩了（1）」分不开，下面就没法分别处理。
  TUNE_BIN="$(mktemp -t upp-sourcetune.XXXXXX)"
  if go build -o "${TUNE_BIN}" ./cmd/sourcetune; then
    TUNE_ARGS=(-redis-db "${REDIS_DB}")
    # 只有 --write 时才真改开关：调的是共享状态，试跑不该有副作用。
    [[ "${WRITE}" == "1" && "${TUNE_APPLY}" == "1" ]] && TUNE_ARGS+=(-apply)
    tune_rc=0
    "${TUNE_BIN}" "${TUNE_ARGS[@]}" || tune_rc=$?
    case "${tune_rc}" in
      0) ;;
      3) echo "sourcetune 拒绝执行：多数源同时读作死，先查校验 URL 和网络，别急着关源" ;;
      *) echo "sourcetune 失败（退出码 ${tune_rc}），不影响已完成的补池" ;;
    esac
  else
    echo "sourcetune 编译失败，跳过这一步"
  fi
  rm -f "${TUNE_BIN}"
fi

# ---- 收尾：报一下池子现状 ----
echo
echo "--- 池子现状 ---"
if command -v redis-cli >/dev/null 2>&1; then
  total="$(redis-cli -n "${REDIS_DB}" SCARD upp:proxies:all 2>/dev/null || echo '?')"
  validated="$(redis-cli -n "${REDIS_DB}" ZCARD upp:proxies:scored 2>/dev/null || echo '?')"
  raw="$(redis-cli -n "${REDIS_DB}" ZCARD upp:proxies:raw 2>/dev/null || echo '?')"
  echo "DB ${REDIS_DB}: total=${total} validated=${validated} raw=${raw}"
else
  echo "没有 redis-cli，跳过"
fi

echo
if [[ "${WRITE}" != "1" ]]; then
  echo "本轮是试跑，没有写任何数据。加 --write 才落库。"
fi
echo "[$(date '+%F %T')] auto-enrich 结束（本轮开始于 ${started}）"
