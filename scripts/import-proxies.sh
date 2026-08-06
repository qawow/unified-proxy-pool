#!/usr/bin/env bash
# 把一份代理清单导入运行中的面板。
#
# 用法：
#   ./scripts/import-proxies.sh --in live.txt
#   ./scripts/import-proxies.sh --in live.txt --panel http://172.18.49.135:7891
#   UPP_PASSWORD=admin ./scripts/import-proxies.sh --in live.txt
#   ./scripts/import-proxies.sh --in live.txt --concurrency 20 --dry-run
#
# 走的是 POST /api/proxies/test：这个接口对未知地址会先 AddRaw 再实测
# （internal/freproxies/service.go 里 TestProxyOpts 的行为），所以一次
# 调用同时完成「入库」和「验活」，不需要另开一个批量导入接口。
#
# 代价是每条一个请求，慢。所以先用 ./scripts/fetch-proxies.sh --check
# 把死的筛掉，再导入活的，别把几万条原始地址直接灌进来。
#
# 鉴权：需要管理员密码（登录拿 Cookie）。密码从 UPP_PASSWORD 读，
# 没设就交互式提示，不接受命令行参数——命令行会进 shell 历史和 ps。

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

IN=""
PANEL="${UPP_PANEL:-http://127.0.0.1:7891}"
CONCURRENCY=10
DRY_RUN=0
LIMIT=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --in)      IN="${2:?--in 需要一个值}"; shift 2 ;;
    --panel)   PANEL="${2:?--panel 需要一个值}"; shift 2 ;;
    --concurrency) CONCURRENCY="${2:?--concurrency 需要一个值}"; shift 2 ;;
    --limit)   LIMIT="${2:?--limit 需要一个值}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) sed -n '2,20p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数：$1（用 --help 看用法）" >&2; exit 2 ;;
  esac
done

if [[ -z "${IN}" ]]; then
  echo "缺少 --in <文件>。先生成一份清单：" >&2
  echo "  ./scripts/fetch-proxies.sh --check --out live.txt" >&2
  exit 2
fi
if [[ ! -r "${IN}" ]]; then
  echo "读不到文件：${IN}" >&2
  exit 1
fi

PANEL="${PANEL%/}"

# 只取每行第一列，兼容 checkproxies -latency 的 "addr<TAB>ms" 输出。
mapfile -t ADDRS < <(awk 'NF && $1 !~ /^#/ {print $1}' "${IN}" | sort -u)
if [[ "${LIMIT}" -gt 0 && "${#ADDRS[@]}" -gt "${LIMIT}" ]]; then
  ADDRS=("${ADDRS[@]:0:${LIMIT}}")
fi

if [[ "${#ADDRS[@]}" == "0" ]]; then
  echo "文件里没有可用地址：${IN}" >&2
  exit 1
fi

echo "面板     : ${PANEL}"
echo "待导入   : ${#ADDRS[@]} 条（来自 ${IN}）"
echo

# --dry-run 先于连通性检查：它的用途就是在不碰面板的前提下确认解析结果，
# 所以不该因为面板没起就失败。
if [[ "${DRY_RUN}" == "1" ]]; then
  echo "--dry-run：只列前 10 条，不发请求"
  printf '  %s\n' "${ADDRS[@]:0:10}"
  [[ "${#ADDRS[@]}" -gt 10 ]] && echo "  …其余 $(( ${#ADDRS[@]} - 10 )) 条"
  exit 0
fi

if ! curl -fsS -m 10 "${PANEL}/api/health" >/dev/null 2>&1; then
  echo "连不上面板 ${PANEL}/api/health" >&2
  echo "先确认服务在跑：make run" >&2
  exit 1
fi

# ---- 登录 ----
PASSWORD="${UPP_PASSWORD:-}"
if [[ -z "${PASSWORD}" ]]; then
  read -rsp "管理员密码: " PASSWORD
  echo
fi
if [[ -z "${PASSWORD}" ]]; then
  echo "密码为空，放弃" >&2
  exit 2
fi

COOKIE_JAR="$(mktemp)"
# 密码和会话 Cookie 都不该留在磁盘上，退出时一并清掉。
trap 'rm -f "${COOKIE_JAR}"' EXIT
chmod 600 "${COOKIE_JAR}"

# 密码经 stdin 交给 python 组装 JSON，避免手拼字符串时被引号/反斜杠破坏。
LOGIN_BODY="$(PASSWORD="${PASSWORD}" python3 -c '
import json, os
print(json.dumps({"password": os.environ["PASSWORD"]}))
')"
unset PASSWORD

if ! curl -fsS -m 15 -c "${COOKIE_JAR}" \
      -H 'Content-Type: application/json' \
      --data-binary "${LOGIN_BODY}" \
      "${PANEL}/api/auth/login" >/dev/null 2>&1; then
  echo "登录失败：密码不对，或面板没开鉴权" >&2
  exit 1
fi
unset LOGIN_BODY
echo "登录成功"
echo

# ---- 逐条导入 ----
# 单条 = 一次 POST /api/proxies/test。用 xargs 控制并发；每条自己拼 JSON，
# 地址里含方括号（IPv6）也不会破坏 payload。
export PANEL COOKIE_JAR

import_one() {
  local addr="$1"
  local body
  body="$(ADDR="${addr}" python3 -c '
import json, os
print(json.dumps({"addr": os.environ["ADDR"]}))
')"
  local resp
  resp="$(curl -sS -m 30 -b "${COOKIE_JAR}" \
          -H 'Content-Type: application/json' \
          --data-binary "${body}" \
          "${PANEL}/api/proxies/test" 2>/dev/null)" || { echo "ERR ${addr}"; return; }
  # data.ok 才算真的活；入库无论如何都已经发生了。
  if RESP="${resp}" python3 -c '
import json, os, sys
try:
    d = json.loads(os.environ["RESP"])
except Exception:
    sys.exit(1)
sys.exit(0 if (d.get("data") or {}).get("ok") else 1)
' 2>/dev/null; then
    echo "OK  ${addr}"
  else
    echo "DEAD ${addr}"
  fi
}
export -f import_one

RESULTS="$(mktemp)"
trap 'rm -f "${COOKIE_JAR}" "${RESULTS}"' EXIT

printf '%s\n' "${ADDRS[@]}" \
  | xargs -P "${CONCURRENCY}" -I {} bash -c 'import_one "$@"' _ {} \
  | tee "${RESULTS}" \
  | awk '{ if (NR % 25 == 0) printf "  已处理 %d 条\n", NR }'

OK_N="$(grep -c '^OK ' "${RESULTS}" || true)"
DEAD_N="$(grep -c '^DEAD ' "${RESULTS}" || true)"
ERR_N="$(grep -c '^ERR ' "${RESULTS}" || true)"

echo
echo "导入完成：${OK_N} 条可用，${DEAD_N} 条不通，${ERR_N} 条请求失败"
echo "全部已入库（不通的会在后续校验轮里被扣分淘汰）"
echo "看结果：curl -s ${PANEL}/api/public/proxies/count"
