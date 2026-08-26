#!/usr/bin/env bash
# 局域网客户端演示：把本机/其他设备流量走 Unified Proxy Pool 的 DirectProxy
# 用法：
#   ./examples/lan-client.sh                 # 自动探测本机局域网 IP
#   ./examples/lan-client.sh 192.168.1.10    # 指定服务器局域网 IP
#   LAN_IP=192.168.1.10 ./examples/lan-client.sh

set -euo pipefail

detect_lan_ip() {
  if command -v hostname >/dev/null 2>&1; then
    local ip
    ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
    if [[ -n "${ip}" ]]; then
      echo "${ip}"
      return
    fi
  fi
  ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}'
}

LAN_IP="${1:-${LAN_IP:-}}"
if [[ -z "${LAN_IP}" ]]; then
  LAN_IP="$(detect_lan_ip || true)"
fi
if [[ -z "${LAN_IP}" ]]; then
  echo "无法探测局域网 IP，请手动传入：./examples/lan-client.sh <服务器LAN_IP>" >&2
  exit 1
fi

PANEL="http://${LAN_IP}:7891"
HTTP_PROXY_URL="http://${LAN_IP}:7892"
SOCKS_PROXY_URL="socks5://${LAN_IP}:7892"
CHAIN_HTTP_URL="http://${LAN_IP}:7893"
CHAIN_SOCKS_URL="socks5://${LAN_IP}:7893"

echo "=== Unified Proxy Pool 局域网客户端 ==="
echo "服务器 LAN IP : ${LAN_IP}"
echo "管理面板      : ${PANEL}"
echo "单跳 HTTP     : ${HTTP_PROXY_URL}"
echo "单跳 SOCKS5   : ${SOCKS_PROXY_URL}"
echo "套代 HTTP     : ${CHAIN_HTTP_URL}   # 代理套代理 2+ 跳"
echo "套代 SOCKS5   : ${CHAIN_SOCKS_URL}"
echo

echo "1) 单跳环境变量"
cat <<EOF
export http_proxy=${HTTP_PROXY_URL}
export https_proxy=${HTTP_PROXY_URL}
export ALL_PROXY=${SOCKS_PROXY_URL}
EOF
echo

echo "2) 代理套代理环境变量"
cat <<EOF
export http_proxy=${CHAIN_HTTP_URL}
export https_proxy=${CHAIN_HTTP_URL}
export ALL_PROXY=${CHAIN_SOCKS_URL}
EOF
echo

echo "3) curl 示例"
echo "curl -x ${HTTP_PROXY_URL} https://httpbin.org/ip          # 单跳"
echo "curl -x ${CHAIN_HTTP_URL} https://httpbin.org/ip          # 套代"
echo "curl --socks5-hostname ${LAN_IP}:7893 https://httpbin.org/ip"
echo

if [[ "${RUN_TEST:-0}" == "1" ]]; then
  echo "3) 实际请求测试 (RUN_TEST=1)"
  curl -sS -m 20 -x "${HTTP_PROXY_URL}" https://httpbin.org/ip || echo "HTTP 代理测试失败（可能尚未有可用免费代理）"
fi

echo
echo "=== 按渠道取代理 + 回传结果 ==="
echo "HTTPS 走 CONNECT，池子看不到里面的 403/429，调用方读完响应要 POST 回去："
echo "  ADDR=\$(curl -sS '${PANEL}/api/public/get?target=https://httpbin.org/ip')"
echo "  curl -sS -X POST '${PANEL}/api/public/channels/report' \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d \"{\\\"target\\\":\\\"https://httpbin.org/ip\\\",\\\"addr\\\":\\\"\${ADDR}\\\",\\\"ok\\\":true,\\\"status\\\":200}\""

echo
echo "Windows CMD:"
echo "  set http_proxy=${HTTP_PROXY_URL}"
echo "  set https_proxy=${HTTP_PROXY_URL}"
echo "macOS/iOS/Android 系统代理：主机 ${LAN_IP} 端口 7892 类型 HTTP 或 SOCKS"
echo "浏览器 SwitchyOmega：HTTP ${LAN_IP} 7892  或 SOCKS5 ${LAN_IP} 7892"
