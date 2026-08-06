#!/usr/bin/env bash
# 往运行中的面板里注册一个自定义采集源（等同于面板「采集源」页的新建）。
#
# 用法：
#   ./scripts/add-source.sh --name my-list --url https://example.com/http.txt
#   ./scripts/add-source.sh --name my-api --url https://example.com/api.json --format json
#   ./scripts/add-source.sh --name my-api --url u1 --url u2 --format jsonl --protocol socks5
#   ./scripts/add-source.sh --name my-table --url https://x/list.html --format html_table \
#                           --host-col 0 --port-col 1
#   ./scripts/add-source.sh --name my-list --url ... --test-only   # 只试解析，不注册
#
# format 取值：plaintext | json | jsonl | html_table | html_regex | socks_list
#   plaintext   —— 正文里直接是 1.2.3.4:8080（也认 [v6]:port）
#   json/jsonl  —— JSON 数组／包装对象／每行一个 JSON；host+port 分开写也能读
#   html_table  —— 从 <table> 里按列取，配 --host-col / --port-col
#
# --test-only 会先用 curl 拉一次 URL、按所选 format 本地解析并报数，
# 确认能出货再注册。免费源换格式很频繁，先试一把比事后查空源省事。

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

NAME=""
URLS=()
FORMAT="plaintext"
PROTOCOL=""
HOST_COL=0
PORT_COL=1
FRAGILE=0
DISABLED=0
TEST_ONLY=0
PANEL="${UPP_PANEL:-http://127.0.0.1:7891}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)     NAME="${2:?--name 需要一个值}"; shift 2 ;;
    --url)      URLS+=("${2:?--url 需要一个值}"); shift 2 ;;
    --format)   FORMAT="${2:?--format 需要一个值}"; shift 2 ;;
    --protocol) PROTOCOL="${2:?--protocol 需要一个值}"; shift 2 ;;
    --host-col) HOST_COL="${2:?--host-col 需要一个值}"; shift 2 ;;
    --port-col) PORT_COL="${2:?--port-col 需要一个值}"; shift 2 ;;
    --fragile)  FRAGILE=1; shift ;;
    --disabled) DISABLED=1; shift ;;
    --test-only) TEST_ONLY=1; shift ;;
    --panel)    PANEL="${2:?--panel 需要一个值}"; shift 2 ;;
    -h|--help)  sed -n '2,22p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数：$1（用 --help 看用法）" >&2; exit 2 ;;
  esac
done

if [[ -z "${NAME}" || "${#URLS[@]}" == "0" ]]; then
  echo "至少要给 --name 和一个 --url" >&2
  echo "例：./scripts/add-source.sh --name my-list --url https://example.com/http.txt" >&2
  exit 2
fi

case "${FORMAT}" in
  plaintext|json|jsonl|html_table|html_regex|socks_list) ;;
  *) echo "不支持的 format：${FORMAT}（见 --help）" >&2; exit 2 ;;
esac

if [[ -z "${PROTOCOL}" ]]; then
  # 与 crawlers.NewDynamic 的默认保持一致。
  if [[ "${FORMAT}" == "socks_list" ]]; then PROTOCOL="socks5"; else PROTOCOL="http"; fi
fi

PANEL="${PANEL%/}"

# ---- 先本地试解析 ----
echo "=== 试抓 ${URLS[0]} ==="
TMP_BODY="$(mktemp)"
trap 'rm -f "${TMP_BODY}"' EXIT

HTTP_CODE="$(curl -sS -m 25 -A 'Mozilla/5.0 (compatible; UnifiedProxyPool/1.0)' \
             -o "${TMP_BODY}" -w '%{http_code}' "${URLS[0]}" 2>/dev/null || echo 000)"
BYTES="$(wc -c < "${TMP_BODY}" | tr -d ' ')"
echo "HTTP ${HTTP_CODE}，${BYTES} 字节"

if [[ "${HTTP_CODE}" != "200" || "${BYTES}" == "0" ]]; then
  echo "拉不到内容，先确认 URL 能公开访问" >&2
  exit 1
fi

# 用 Go 侧真正的解析器数一遍，不在 shell 里另写一套正则——
# 否则脚本说「能解析」而面板解析不出来，等于白测。
PARSED="$(FORMAT="${FORMAT}" PROTOCOL="${PROTOCOL}" \
          HOST_COL="${HOST_COL}" PORT_COL="${PORT_COL}" BODY_FILE="${TMP_BODY}" \
          go run ./cmd/testsource 2>&1)" || {
  echo "解析失败：${PARSED}" >&2
  exit 1
}
echo "${PARSED}"

COUNT="$(printf '%s' "${PARSED}" | awk '/^parsed=/{sub(/^parsed=/,""); print; exit}')"
if [[ -z "${COUNT}" || "${COUNT}" == "0" ]]; then
  echo
  echo "这个 format 从该 URL 解析出 0 条，换个 format 再试：" >&2
  echo "  json / jsonl —— 正文是 JSON 时用" >&2
  echo "  html_table --host-col N --port-col M —— 正文是 HTML 表格时用" >&2
  exit 1
fi

if [[ "${TEST_ONLY}" == "1" ]]; then
  echo
  echo "--test-only：解析出 ${COUNT} 条，未注册。去掉该参数即可写入面板。"
  exit 0
fi

# ---- 注册到面板 ----
if ! curl -fsS -m 10 "${PANEL}/api/health" >/dev/null 2>&1; then
  echo "连不上面板 ${PANEL}/api/health（服务在跑吗：make run）" >&2
  exit 1
fi

PASSWORD="${UPP_PASSWORD:-}"
if [[ -z "${PASSWORD}" ]]; then
  read -rsp "管理员密码: " PASSWORD
  echo
fi

COOKIE_JAR="$(mktemp)"
trap 'rm -f "${TMP_BODY}" "${COOKIE_JAR}"' EXIT
chmod 600 "${COOKIE_JAR}"

LOGIN_BODY="$(PASSWORD="${PASSWORD}" python3 -c '
import json, os
print(json.dumps({"password": os.environ["PASSWORD"]}))
')"
unset PASSWORD

if ! curl -fsS -m 15 -c "${COOKIE_JAR}" -H 'Content-Type: application/json' \
      --data-binary "${LOGIN_BODY}" "${PANEL}/api/auth/login" >/dev/null 2>&1; then
  echo "登录失败" >&2
  exit 1
fi
unset LOGIN_BODY

# URL 数组交给 python 序列化，避免手拼 JSON 被引号和 & 破坏。
BODY="$(NAME="${NAME}" FORMAT="${FORMAT}" PROTOCOL="${PROTOCOL}" \
        HOST_COL="${HOST_COL}" PORT_COL="${PORT_COL}" \
        FRAGILE="${FRAGILE}" DISABLED="${DISABLED}" \
        URLS_JOINED="$(printf '%s\n' "${URLS[@]}")" python3 -c '
import json, os
print(json.dumps({
    "name":     os.environ["NAME"],
    "urls":     [u for u in os.environ["URLS_JOINED"].splitlines() if u.strip()],
    "format":   os.environ["FORMAT"],
    "protocol": os.environ["PROTOCOL"],
    "enabled":  os.environ["DISABLED"] != "1",
    "fragile":  os.environ["FRAGILE"] == "1",
    "host_col": int(os.environ["HOST_COL"]),
    "port_col": int(os.environ["PORT_COL"]),
}))
')"

RESP="$(curl -sS -m 20 -b "${COOKIE_JAR}" -H 'Content-Type: application/json' \
        --data-binary "${BODY}" "${PANEL}/api/scrapers" 2>/dev/null)"

if RESP="${RESP}" python3 -c '
import json, os, sys
d = json.loads(os.environ["RESP"])
if not d.get("success"):
    print("面板拒绝：" + (d.get("message") or "未知原因"), file=sys.stderr)
    sys.exit(1)
' ; then
  echo
  echo "已注册采集源 ${NAME}（${FORMAT} / ${PROTOCOL} / ${#URLS[@]} 个 URL）"
  echo "立刻跑一次： curl -sS -b <cookie> -X POST ${PANEL}/api/scrapers/${NAME}/run"
  echo "或在面板「采集源」页点运行。"
else
  exit 1
fi
