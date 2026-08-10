# Unified Proxy Pool

融合 **Super-Proxy-Pool** 与多个 Python 免费代理池能力的统一 **Go + React** 代理池面板。

单二进制部署，支持局域网面板、免费代理采集/校验、**单跳 DirectProxy** 与 **多跳链式代理**、订阅/出口池（Mihomo）、运维与可观测接口。

## 功能概览

| 模块 | 能力 |
|------|------|
| 管理面板 | React 玻璃拟态 UI · 亮/暗主题 · 仪表盘卡片可开关 |
| 免费代理 | 80+ 采集源 · 评分校验 · Redis 优先（降级 memory）· GeoIP · 支持采集源删除（自定义源） |
| AI 入池 | JSON / 纯文本批量导入 · session 或 Bearer Token · 来源标记 · 重复/无效项反馈 |
| **链式代理** | `7893` 多跳 · 跳数/容错/去重/协议地区/粘性/认证可配；成员来自出口池选择 |
| 出口池 | 支持按类型一键选择节点/订阅/免费代理；链式、单跳均使用出口池成员 |
| 可观测 | 入站/出站连接 · `/metrics` · 流量采样 · Webhook 告警 |
| 说明接口 | `/api/explain?q=xxx` 返回代理池功能说明，支持 AI 其他调用 |

## 默认端口（局域网）

| 服务 | 绑定 | 访问示例 |
|------|------|----------|
| 管理面板 + Mux | `0.0.0.0:7891` | `http://<LAN_IP>:7891` |
| DirectProxy 单跳 | `0.0.0.0:7892` | `http://<LAN_IP>:7892` / `socks5://…` |
| **链式代理** | `0.0.0.0:7893` | `http://<LAN_IP>:7893` |
| 默认登录 | 用户可忽略，密码 `admin` | 登录后请改密 |

```bash
hostname -I | awk '{print $1}'   # 例如 172.18.49.135
```

## 快速启动

### 本地

推荐先构建前端：

```bash
cd frontend && npm install && npm run build && cd ..
export GOPROXY=https://goproxy.cn,direct
export DATA_DIR=./data
go build -buildvcs=false -o unified-proxy-pool ./cmd/app
./unified-proxy-pool
```

# 注意：Mihomo 二进制可选，开启代理功能需提供（Docker 中已自动下载）

# 或直接：
# go run -buildvcs=false ./cmd/app

- 面板：`http://<LAN_IP>:7891`
- 单跳：`http://<LAN_IP>:7892`
- 链式：`http://<LAN_IP>:7893`

### Docker

使用 `docker compose`（包含 Redis）：

```bash
docker compose up -d --build
```

Dockerfile 自动构建前端 + Go + 下载 Mihomo（v1.19.22）。

推荐：
- 端口映射模式：`docker compose up -d`
- Host 网络模式：`docker compose --profile host up -d unified-proxy-pool-host`

面板访问：`http://localhost:7891`

## 局域网客户端

```bash
chmod +x examples/lan-client.sh
./examples/lan-client.sh                 # 本机生成命令
./examples/lan-client.sh 172.18.49.135   # 指定服务器 IP
RUN_TEST=1 ./examples/lan-client.sh 172.18.49.135
```

### 单跳

```bash
export http_proxy=http://172.18.49.135:7892
export https_proxy=http://172.18.49.135:7892
export ALL_PROXY=socks5://172.18.49.135:7892
curl -x http://172.18.49.135:7892 https://httpbin.org/ip
```

### 链式代理（多跳）

```bash
# 流量：本机 → 入口 → [中继…] → 出口 → 目标
curl -x http://172.18.49.135:7893 https://httpbin.org/ip
export http_proxy=http://172.18.49.135:7893 https_proxy=http://172.18.49.135:7893
```

面板 **设置 → 链式代理** 可配置跳数、容错、超时、去重 Host/地区、入口/出口协议与地区、粘性、认证、CIDR、限速，并支持「立即应用」与客户端脚本下载。

## 环境变量

| 变量 | 默认 | 说明 |
|------|------|------|
| `PANEL_HOST` / `PANEL_PORT` | `0.0.0.0` / `7891` | 面板 |
| `DATA_DIR` | `./data` | 数据与 SQLite |
| `REDIS_ADDR` | `127.0.0.1:6379` | 不可用则 memory |
| `SESSION_MAX_AGE_SEC` | `604800` | 登录会话（7 天） |
| `FREE_PROXY_ENABLED` | `true` | 免费代理管道 |
| `SCRAPE_INTERVAL_SEC` | `300` | 采集周期 |
| `VALIDATE_INTERVAL_SEC` | `120` | 校验周期 |
| `FREE_VALIDATE_URL` | gstatic 204 | 校验探测 URL |
| `FREE_VALIDATE_CONCURRENCY` | `32` | 校验并发 |
| `DIRECT_PROXY_ENABLED` | `true` | 单跳出口 |
| `DIRECT_PROXY_ADDR` | `0.0.0.0:7892` | 单跳监听 |
| `DIRECT_PROXY_USERNAME/PASSWORD` | 空 | 可选认证 |
| `PROXY_CHAIN_ENABLED` | `true` | 链式出口 |
| `PROXY_CHAIN_ADDR` | `0.0.0.0:7893` | 链式监听 |
| `PROXY_CHAIN_HOPS` | `2` | 默认跳数 2–4 |

更多运行参数可在面板 **系统设置** / `feature_json` 中热更新（采集校验周期、Webhook、仪表盘卡片、链式策略等）。

## 页面

| 路由 | 说明 |
|------|------|
| `/` | 仪表盘（实时连接入站/出站、链式路径、卡片可配置） |
| `/proxies` | 代理池（连接串复制、详情、导出、清理、拉黑） |
| `/sources` | 采集源 |
| `/validator` | 校验统计 + **实时日志** |
| `/subscriptions` | 订阅 |
| `/nodes` | 手动节点 |
| `/ai-proxy` | AI / 脚本代理批量入池 |
| `/explain` | 获取代理池功能说明（支持 AI 调用） |
| `/settings` | 系统设置 · 链式详细配置 · Token · 客户端脚本 |

## 公开 API（无登录）

```bash
curl http://LAN:7891/api/public/get
curl 'http://LAN:7891/api/public/get?format=json&region=US&proto=http'
curl http://LAN:7891/api/public/count
curl http://LAN:7891/api/public/health
curl http://LAN:7891/metrics
```

完整接口见 [docs/API.md](docs/API.md)，变更记录见 [CHANGELOG.md](CHANGELOG.md)。

## 开发

```bash
cd frontend && npm run dev    # 5173，代理到 7891
go test -buildvcs=false ./...
go build -buildvcs=false -o unified-proxy-pool ./cmd/app
```

前端构建产物嵌入 `web/dist`，随 Go 二进制发布。

## 架构要点

```
客户端 ──► :7892 单跳 ──► 免费代理 ──► 目标
客户端 ──► :7893 链式 ──► 入口 → … → 出口 ──► 目标
浏览器 ──► :7891 面板/API/Mux 池代理
                │
        freproxies (Redis/memory) + validator + crawlers
                │
        SQLite (settings / sessions / audit / tokens / …)
```

- **选路**：进程内 HotCache（约 3s）优先，降低 Redis 读放大  
- **列表**：已验证分页走 ZSET 窗口 + MGET，避免全库 N+1  
- **流量**：`active_in` / `active_out` 与通道统计  

## 参考项目

- Super-Proxy-Pool  
- jhao104/proxy_pool  
- Python3WebSpider/ProxyPool  
- scylla / haipproxy  

## License

按仓库内声明；衍生自 Super-Proxy-Pool 与开源代理池生态，请遵守各上游协议。
