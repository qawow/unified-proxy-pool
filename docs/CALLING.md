# 按接口调用

局域网默认：面板 `7891` · 单跳 `7892` · 链式 `7893`。把 `HOST` 换成软路由 IP。

`/api/public/*` **默认只允许局域网**（`10/8` `172.16/12` `192.168/16` 环回）。公网 IP 会 403。需要额外网段填设置里的 `allowed_cidrs`；确要暴露公网再勾 `public_open`（危险）。`X-Forwarded-For` 只在对端是本机时才信。

```bash
HOST=192.168.2.198
```

一键打印命令：`./examples/call.sh $HOST`

---

## 1. 取一条免费代理（脚本 / 爬虫）

不登录。返回 `host:port` 或 JSON。

```bash
curl -s "http://$HOST:7891/api/public/get"                  # 每次换新
curl -s "http://$HOST:7891/api/public/get?sticky=1"         # 按客户端 IP 复用（长连接）
curl -s "http://$HOST:7891/api/public/get?session=job-1"    # 按会话名复用
curl -s "http://$HOST:7891/api/public/get?session=job-1&refresh=1"  # 强制换新并更新会话
curl -s "http://$HOST:7891/api/public/get?format=json&proto=http&region=US"
curl -s "http://$HOST:7891/api/public/get?count=5"
curl -s "http://$HOST:7891/api/public/count"
curl -s "http://$HOST:7891/api/public/debug"           # 池子/7892/7893/mihomo probe 是否活着、上次异常退出
curl -s "http://$HOST:7891/api/public/debug/mihomo"
```

用这条代理访问目标：

```bash
ADDR=$(curl -s "http://$HOST:7891/api/public/get")
curl -x "http://$ADDR" https://httpbin.org/ip
```

## 2. 单跳出网（本机流量走池子）

面板里的 DirectProxy，自动轮换免费代理。

```bash
export http_proxy=http://$HOST:7892 https_proxy=http://$HOST:7892
export ALL_PROXY=socks5://$HOST:7892
curl -x http://$HOST:7892 https://httpbin.org/ip
```

## 3. 经小 VPS 再走代理（出口 IP = 代理 IP）

7892/7893 默认家里直连免费代理。要让**代理流量先到你的 VPS，再从代理节点出网**（对方看到的是代理 IP，不是 VPS、也不是家里）：

```
本机 → 7892/7893 → 小 VPS → 代理节点 → 网站
```

1. VPS 上开 SOCKS5（`ssh -D 0.0.0.0:1080` 或 microsocks / xray），安全组放行该端口。
2. 面板 **设置 → 链式代理 → 固定 VPS** 填：

```
socks5://user:pass@VPS公网IP:1080
```

3. **VPS 位置**选 **第一跳**（出口 IP = 代理）。不要选最后一跳，那会变成网站看到 VPS IP。
4. 保存（热更新）。测：

```bash
curl -x http://$HOST:7892 https://httpbin.org/ip
```

这里应显示**池子里那条代理的 IP**，不是 VPS、也不是家里宽带。

VPS 必须能访问那些代理；家里只需要能连上 VPS。

## 4. 链式出网（多跳）

入口 → 中继 → 出口。跳数在面板「设置 → 链式代理」。

```bash
curl -x http://$HOST:7893 https://httpbin.org/ip
```

## 5. 入池（脚本提交）

```bash
printf '1.2.3.4:8080\nsocks5://user:pass@[2001:db8::1]:1080\n' \
  | curl -sS -X POST "http://$HOST:7891/api/public/submit?source=my-job" \
         -H 'Content-Type: text/plain' --data-binary @-
```

Clash / `ss://` / `hy2://` / `vless://` 不要走这个接口，走下面第 5 条。

## 6. 订阅 / 手动节点（VLESS、HY2、SS）

面板登录后：

- 订阅 URL 填 **局域网或公网地址**，不要填 Docker 主机名（会 `no such host`）
- **获取时套代理**：`direct`（本机 7892）、`chain`（7893）、或 `socks5://user:pass@host:port`
- 请求头按链接自动选：Clash YAML → `clash.meta`；GitHub raw → clash.meta；机场 `subscribe?token=` → `v2rayN`。自定义 JSON 头可覆盖
- 手动节点可粘贴 URI 或 YAML：`ss://` `hy2://` `hysteria2://` `vless://` `trojan://` `vmess://`

```bash
# 需要登录 Cookie 或 Bearer
curl -s -c /tmp/upp.txt -X POST "http://$HOST:7891/api/auth/login" \
  -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin"}'
curl -s -b /tmp/upp.txt -X POST "http://$HOST:7891/api/manual-nodes" \
  -H 'Content-Type: application/json' \
  -d '{"content":"hy2://password@example.com:443?insecure=1#hy2-demo"}'
```

测存活：节点卡片上的延迟测试，或：

```bash
curl -s -b /tmp/upp.txt -X POST "http://$HOST:7891/api/manual-nodes/<id>/latency-test"
```

## 7. 渠道封禁回传

HTTPS 走 CONNECT，池子看不到 403/429，调用方读完响应要回报：

```bash
curl -s -X POST "http://$HOST:7891/api/public/channels/report" \
  -H 'Content-Type: application/json' \
  -d '{"target":"https://item.taobao.com/x","addr":"1.2.3.4:8080","ok":false,"status":403}'
```

---

## 存活测试怎么走

| 节点类型 | 测法 | 不依赖 Mihomo |
|----------|------|----------------|
| http / https / socks5 | 经代理 GET 探测 URL | 是 |
| socks5 + tls | TCP + TLS 握手 | 是 |
| ss / vless / trojan / hy2 / vmess | 先 TCP，再 Mihomo delay | TCP 阶段否 |

失败信息会写成 `tcp host:port: ...` 或 `mihomo delay: ...`，不再只是笼统的 Timeout。
