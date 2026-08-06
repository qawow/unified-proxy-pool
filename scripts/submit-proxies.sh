#!/usr/bin/env bash
# 通过 HTTP API 把代理提交进池子。跟 scan-to-pool.sh 的区别是：
# 这个不需要 Redis 权限，只要能访问面板，所以能从别的机器上跑。
#
# 用法：
#   ./scripts/submit-proxies.sh -f proxies.txt              # 从文件提交
#   cat proxies.txt | ./scripts/submit-proxies.sh           # 从管道提交
#   ./scripts/submit-proxies.sh -f x.txt --test             # 提交后再验活
#   ./scripts/submit-proxies.sh -f x.txt --token upp_xxx    # 用 API token
#   ./scripts/submit-proxies.sh -f x.txt --public           # 走免鉴权接口
#
# 输入格式（每行一条，井号开头的行和空行会跳过）：
#   1.2.3.4:8080
#   http://5.6.7.8:3128
#   socks5://user:pass@9.10.11.12:1080
#   [::1]:1080
#
# 鉴权三选一，按优先级：
#   1. --token / UPP_TOKEN     Authorization: Bearer，脚本首选
#   2. --cookie / UPP_COOKIE   已登录的 session cookie
#   3. --public                走 /api/public/submit，不鉴权（仅限内网）
#
# 两个取舍：
#
# 1. 提交和验活分开。--test 是可选的，因为一次提交几千条、验活要几分钟，
#    而入池本身是瞬间的事。不带 --test 就交给面板的定时校验轮去打分。
#
# 2. 只有 --test 才会真的动分数。提交进去的是原始代理（ScoreInit），
#    面板照常会淘汰它们；想立刻确认哪些能用就加 --test。

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

PANEL="${UPP_PANEL:-http://127.0.0.1:7891}"
TOKEN="${UPP_TOKEN:-}"
COOKIE="${UPP_COOKIE:-}"
INFILE=""
SOURCE="script-submit"
DO_TEST=0
USE_PUBLIC=0
BATCH=500
TEST_CONCURRENCY=20
TEST_TIMEOUT_MS=8000
# 200 是服务端 Service.BatchTest 的 maxItems，超了会被静默截断。
TEST_BATCH=200

while [[ $# -gt 0 ]]; do
  case "$1" in
    -f|--file)      INFILE="${2:?-f 需要一个文件路径}"; shift 2 ;;
    --panel)        PANEL="${2:?--panel 需要一个值}"; shift 2 ;;
    --token)        TOKEN="${2:?--token 需要一个值}"; shift 2 ;;
    --cookie)       COOKIE="${2:?--cookie 需要一个值}"; shift 2 ;;
    --source)       SOURCE="${2:?--source 需要一个值}"; shift 2 ;;
    --batch)        BATCH="${2:?--batch 需要一个值}"; shift 2 ;;
    --test)         DO_TEST=1; shift ;;
    --public)       USE_PUBLIC=1; shift ;;
    --concurrency)  TEST_CONCURRENCY="${2:?--concurrency 需要一个值}"; shift 2 ;;
    --timeout-ms)   TEST_TIMEOUT_MS="${2:?--timeout-ms 需要一个值}"; shift 2 ;;
    -h|--help)      awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数：$1（用 --help 看用法）" >&2; exit 2 ;;
  esac
done

PANEL="${PANEL%/}"

if ! command -v curl >/dev/null 2>&1; then
  echo "需要 curl" >&2
  exit 1
fi

# ---- 读入并清理 ----
raw="$(mktemp)"; trap 'rm -f "${raw}" "${clean}" "${resp}"' EXIT
if [[ -n "${INFILE}" ]]; then
  [[ -r "${INFILE}" ]] || { echo "读不到文件：${INFILE}" >&2; exit 1; }
  cat -- "${INFILE}" > "${raw}"
else
  # 没给 -f 就从标准输入读；交互式终端下直接报错，免得看起来像卡住了。
  if [[ -t 0 ]]; then
    echo "没有输入。用 -f <文件> 或者从管道喂进来（--help 看用法）" >&2
    exit 2
  fi
  cat > "${raw}"
fi

clean="$(mktemp)"
# 去掉注释、空行、行尾空白和重复项。服务端也会去重，这里先做一遍是为了
# 让下面报的条数是真实提交量，而不是文件行数。
sed -e 's/[[:space:]]*$//' -e '/^[[:space:]]*#/d' -e '/^[[:space:]]*$/d' "${raw}" \
  | sort -u > "${clean}"

count="$(wc -l < "${clean}" | tr -d ' ')"
if [[ "${count}" == "0" ]]; then
  echo "输入里没有有效地址"
  exit 0
fi

# ---- 组鉴权参数 ----
AUTH_ARGS=()
if [[ "${USE_PUBLIC}" == "1" ]]; then
  ENDPOINT="${PANEL}/api/public/submit"
elif [[ -n "${TOKEN}" ]]; then
  ENDPOINT="${PANEL}/api/proxies/submit"
  AUTH_ARGS+=(-H "Authorization: Bearer ${TOKEN}")
elif [[ -n "${COOKIE}" ]]; then
  ENDPOINT="${PANEL}/api/proxies/submit"
  AUTH_ARGS+=(-b "${COOKIE}")
else
  echo "需要鉴权：--token / --cookie 三选一，或者用 --public 走免鉴权接口" >&2
  echo "（内网自用一般 --public 最省事；--help 有说明）" >&2
  exit 2
fi

echo "提交 ${count} 条到 ${ENDPOINT}（每批 ${BATCH} 条，source=${SOURCE}）"

resp="$(mktemp)"
total_added=0
total_submitted=0
total_evicted=0
total_growth=0
batch_no=0

# 分批提交：一次几万条会撑爆请求体上限（服务端 public 是 512KB、
# 鉴权接口是 1MB），而且分批能在中途失败时看出提交到哪了。
while IFS= read -r -d '' chunk_file; do
  batch_no=$((batch_no + 1))
  http_code="$(curl -sS -o "${resp}" -w '%{http_code}' \
    -X POST "${ENDPOINT}?source=${SOURCE}" \
    -H 'Content-Type: text/plain' \
    "${AUTH_ARGS[@]}" \
    --data-binary "@${chunk_file}" 2>/dev/null || echo "000")"

  if [[ "${http_code}" != "200" ]]; then
    echo "第 ${batch_no} 批失败（HTTP ${http_code}）：$(head -c 300 "${resp}")" >&2
    case "${http_code}" in
      401) echo "  鉴权没通过。token 过期了？或者试 --public" >&2 ;;
      000) echo "  连不上 ${PANEL}，面板在跑吗？" >&2 ;;
    esac
    rm -f "${chunk_file}"
    exit 1
  fi

  added="$(grep -o '"added":[0-9]*' "${resp}" | head -1 | cut -d: -f2)"
  parsed="$(grep -o '"parsed":[0-9]*' "${resp}" | head -1 | cut -d: -f2)"
  evicted="$(grep -o '"evicted":[0-9]*' "${resp}" | head -1 | cut -d: -f2)"
  growth="$(grep -o '"net_growth":-\?[0-9]*' "${resp}" | head -1 | cut -d: -f2)"
  total_added=$((total_added + ${added:-0}))
  total_submitted=$((total_submitted + ${parsed:-0}))
  total_evicted=$((total_evicted + ${evicted:-0}))
  total_growth=$((total_growth + ${growth:-0}))
  echo "  第 ${batch_no} 批：解析 ${parsed:-0} 条，新增 ${added:-0} 条，池子净增 ${growth:-0} 条"
  rm -f "${chunk_file}"
done < <(
  split -l "${BATCH}" "${clean}" "${TMPDIR:-/tmp}/upp-submit-chunk." \
    --additional-suffix=.txt 2>/dev/null \
    || split -l "${BATCH}" "${clean}" "${TMPDIR:-/tmp}/upp-submit-chunk."
  find "${TMPDIR:-/tmp}" -maxdepth 1 -name 'upp-submit-chunk.*' -print0
)

echo
echo "提交完成：解析 ${total_submitted} 条，新增 ${total_added} 条，池子净增 ${total_growth} 条"
if [[ "${total_added}" -lt "${total_submitted}" ]]; then
  dup=$((total_submitted - total_added))
  echo "（${dup} 条池子里已经有了，服务端按 addr 去重）"
fi
if [[ "${total_evicted}" -gt 0 ]]; then
  echo
  echo "注意：原始池已经到上限（MaxRawProxies=4000），为了放进这批，淘汰了 ${total_evicted} 条已有代理。"
  echo "所以「新增 ${total_added} 条」不等于池子涨了 ${total_added} 条 —— 实际只涨了 ${total_growth} 条。"
  echo "淘汰的是分数最低的，可能是别的源的代理。要真正扩容得先清掉不通的，或者调高上限。"
fi

# ---- 可选：验活 ----
if [[ "${DO_TEST}" == "1" ]]; then
  if [[ "${USE_PUBLIC}" == "1" ]]; then
    echo
    echo "跳过验活：批量验活接口要鉴权，--public 用不了。"
    echo "想验活就换 --token 或 --cookie。"
    exit 0
  fi

  echo
  echo "验活刚提交的地址（每批 ${TEST_BATCH} 条）"
  # 200 是 Service.BatchTest 里的 maxItems，超了会被静默截断，所以这里主动
  # 按 200 切。用 split 切文件，不用 xargs/paste 拼行：实测那种管道会把
  # 每个地址拆成单独一行，结果一条地址发一个请求 —— 3000 条就是 3000 次
  # 往返，而不是 15 次。
  tested=0; alive=0; test_batch_no=0
  test_prefix="${TMPDIR:-/tmp}/upp-test-chunk.$$."
  rm -f "${test_prefix}"* 2>/dev/null || true
  split -l "${TEST_BATCH}" "${clean}" "${test_prefix}"

  for chunk in "${test_prefix}"*; do
    [[ -f "${chunk}" ]] || continue
    test_batch_no=$((test_batch_no + 1))
    n_in_chunk="$(wc -l < "${chunk}" | tr -d ' ')"

    # 拼 JSON：去掉 scheme 和 userinfo，只留 host:port，因为 batch-test
    # 收的是裸地址。
    payload="$(awk 'BEGIN{printf "{\"addrs\":["}
      {gsub(/^[a-z0-9]+:\/\//,""); gsub(/^[^@]*@/,"");
       printf "%s\"%s\"", (NR>1?",":""), $0}
      END{printf "]"}' "${chunk}")"
    payload="${payload},\"concurrency\":${TEST_CONCURRENCY},\"timeout_ms\":${TEST_TIMEOUT_MS}}"

    http_code="$(curl -sS -o "${resp}" -w '%{http_code}' \
      -X POST "${PANEL}/api/proxies/batch-test" \
      -H 'Content-Type: application/json' \
      "${AUTH_ARGS[@]}" \
      --data-binary "${payload}" 2>/dev/null || echo "000")"
    if [[ "${http_code}" != "200" ]]; then
      echo "  第 ${test_batch_no} 批验活失败（HTTP ${http_code}）：$(head -c 200 "${resp}")" >&2
      rm -f "${chunk}"
      continue
    fi
    ok="$(grep -o '"ok":[0-9]*' "${resp}" | tail -1 | cut -d: -f2)"
    fail="$(grep -o '"fail":[0-9]*' "${resp}" | tail -1 | cut -d: -f2)"
    got=$(( ${ok:-0} + ${fail:-0} ))
    tested=$((tested + got))
    alive=$((alive + ${ok:-0}))
    echo "  第 ${test_batch_no} 批：送 ${n_in_chunk} 条，测了 ${got} 条，活 ${ok:-0} 条"
    if [[ "${got}" -lt "${n_in_chunk}" ]]; then
      echo "    （服务端只测了 ${got} 条，上限是 200 —— 这批被截断了）" >&2
    fi
    rm -f "${chunk}"
  done

  echo
  if [[ "${tested}" -gt 0 ]]; then
    echo "验活完成：测了 ${tested} 条，活 ${alive} 条（$((alive * 100 / tested))%）"
    echo "活的已经进 validated，死的按面板规则扣分或删掉。"
  else
    echo "没测到任何地址"
  fi
fi
