#!/usr/bin/env bash
# 按接口打印/执行调用。用法：
#   ./examples/call.sh                  # 探测本机 IP 并打印命令
#   ./examples/call.sh 192.168.2.198    # 指定软路由
#   ./examples/call.sh 192.168.2.198 get
#   ./examples/call.sh 192.168.2.198 direct
#   ./examples/call.sh 192.168.2.198 chain
#   ./examples/call.sh 192.168.2.198 health
set -euo pipefail

detect_ip() {
  hostname -I 2>/dev/null | awk '{print $1}'
}

if [[ "${1:-}" == *.* ]]; then
  HOST="$1"
  shift || true
else
  HOST="${HOST:-$(detect_ip)}"
fi
CMD="${1:-help}"
PANEL="http://${HOST}:7891"

case "$CMD" in
  get)
    echo "# 取一条免费代理"
    curl -sS "${PANEL}/api/public/get"
    echo
    ;;
  health)
    curl -sS "${PANEL}/api/public/health"; echo
    curl -sS "${PANEL}/api/public/count"; echo
    ;;
  debug)
    curl -sS "${PANEL}/api/public/debug"; echo
    ;;
  direct)
    echo "curl -x http://${HOST}:7892 https://httpbin.org/ip"
    curl -sS -m 20 -x "http://${HOST}:7892" https://httpbin.org/ip || true
    echo
    ;;
  chain)
    echo "curl -x http://${HOST}:7893 https://httpbin.org/ip"
    curl -sS -m 20 -x "http://${HOST}:7893" https://httpbin.org/ip || true
    echo
    ;;
  help|*)
    cat <<EOF
Unified Proxy Pool 调用速查  HOST=${HOST}

  取代理     curl -s ${PANEL}/api/public/get
  JSON       curl -s '${PANEL}/api/public/get?format=json&proto=http'
  入池       printf '1.2.3.4:8080\\n' | curl -T - ${PANEL}/api/public/submit?source=cli
  单跳       curl -x http://${HOST}:7892 https://httpbin.org/ip
  链式       curl -x http://${HOST}:7893 https://httpbin.org/ip
  健康       curl -s ${PANEL}/api/public/health
  调试       curl -s ${PANEL}/api/public/debug

子命令:  $0 [HOST] get|health|debug|direct|chain
说明:    /api/public 默认仅局域网；公网 403。docs/CALLING.md
EOF
    ;;
esac
