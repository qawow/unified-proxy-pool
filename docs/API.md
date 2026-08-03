# Unified Proxy Pool API

统一响应：

```json
{ "success": true, "data": {}, "message": "" }
```

- 列表空数据返回 `[]`，不返回 `null`
- 管理接口：登录 Cookie `spp_session`，或（若已创建）`Authorization: Bearer upp_…`
- 公开接口无需登录（适合局域网脚本）

---

## 公开（无鉴权）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/health` | 健康检查（含 validated / 出口状态） |
| GET | `/api/public/health` | 同上 |
| GET | `/api/public/get` | 纯文本 `host:port`（兼容 proxy_pool） |
| GET | `/api/public/get?format=json&proto=&region=` | JSON；可筛协议/地区 |
| GET | `/api/public/proxies/random?proto=&region=` | 随机可用代理 JSON |
| GET | `/api/public/proxies/count` | `{ total, validated, raw, count }` |
| GET | `/api/public/count` | 同 count |
| POST | `/api/public/report` | Body `{ "addr", "ok", "latency_ms?" }` 质量反馈 |
| GET | `/metrics` | Prometheus 文本指标 |

```bash
curl http://172.18.49.135:7891/api/public/get
curl 'http://172.18.49.135:7891/api/public/get?format=json&region=US'
curl http://172.18.49.135:7891/metrics
```

---

## 鉴权

| 方法 | 路径 | Body |
|------|------|------|
| POST | `/api/auth/login` | `{"password":"admin"}` |
| POST | `/api/auth/logout` | |
| GET | `/api/auth/me` | |
| POST | `/api/auth/change-password` | `{"old_password","new_password"}` |

---

## 仪表盘 / 流量

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/overview` | 汇总；含 `traffic.active_in/out` |
| GET | `/api/stats/traffic` | 流量快照 |
| GET | `/api/stats/traffic/history?hours=24` | 历史采样点 |
| GET | `/api/stats/connections` | 活跃连接列表 |
| GET | `/api/health-board` | 免费池 + Direct/链式 + 源质量 |

`TrafficSnapshot` 字段：`up_bytes`, `down_bytes`, `connections`, `success`, `fail`, `active_conns`, **`active_in`**, **`active_out`**, `by_channel`.

---

## 免费代理

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/proxies?page&size&q&proto&source&only_ok&region` | 分页列表 |
| GET | `/api/proxies/export?format=txt\|url\|json&…` | 按筛选导出 |
| POST | `/api/proxies/purge` | 条件清理，见下 |
| GET | `/api/proxies/random?proto=` | 随机 |
| GET | `/api/proxies/count` | 计数 |
| POST | `/api/proxies/test` | `{"addr":"1.2.3.4:8080"}` |
| DELETE | `/api/proxies?proxy=1.2.3.4:8080` | 删除 |

### purge

```json
{
  "only_invalid": true,
  "min_score": 0,
  "max_fail": 0,
  "region": "",
  "source": "",
  "older_than_sec": 0,
  "dry_run": true
}
```

返回：`{ matched, deleted, dry_run, sample }`。

---

## 黑名单

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/blacklist` | 列表 |
| POST | `/api/blacklist` | `{"host"|"addr", "reason?", "ttl_sec?"}` |
| DELETE | `/api/blacklist?host=` | 移除 |

拉黑后 `Pick` / public get 会跳过。

---

## 采集源 / 校验

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/scrapers` | 源列表 |
| POST | `/api/scrapers` | 自定义源 |
| PUT/DELETE | `/api/scrapers/{name}` | 更新/删除自定义 |
| POST | `/api/scrapers/{name}/toggle` | 启停 |
| POST | `/api/scrapers/{name}/run` | 异步跑单源 |
| POST | `/api/scrapers/run-all` | 异步全开源 |
| GET | `/api/scrapers/stats` | 源成功率等 |
| GET | `/api/validator/queues` | 队列/评分分布 |
| POST | `/api/validator/run` | 触发一批校验 |
| GET | `/api/validator/logs?limit=100` | **校验简易日志** |
| POST | `/api/validator/logs/clear` | 清空日志 ring |
| GET | `/api/geo` | 地区 Top |

---

## DirectProxy / 链式代理

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/direct-proxy/status` | 单跳+链式状态、示例、**chain_options** |
| PUT | `/api/direct-proxy/chain` | 更新链式策略（见下） |
| GET | `/api/direct-proxy/client-pack?os=linux\|windows&mode=single\|chain` | 客户端脚本 |

### 出口端口

| 端口 | 模式 | 路径 |
|------|------|------|
| 7892 | 单跳 | 客户端 → 1 免费代理 → 目标 |
| 7893 | **链式代理** | 客户端 → 入口 → … → 出口 → 目标 |

```bash
curl -x http://172.18.49.135:7892 https://httpbin.org/ip   # 单跳
curl -x http://172.18.49.135:7893 https://httpbin.org/ip   # 链式
```

### PUT `/api/direct-proxy/chain`

兼容旧版：`{"hops": 2|3|4}`。

完整示例：

```json
{
  "enabled": true,
  "listen_addr": "0.0.0.0:7893",
  "hops": 3,
  "failover_tries": 6,
  "dial_timeout_ms": 8000,
  "hop_timeout_ms": 5000,
  "prefer_distinct_host": true,
  "prefer_distinct_region": false,
  "entry_proto": "http",
  "exit_proto": "socks5",
  "entry_region": "",
  "exit_region": "",
  "sticky_enabled": false,
  "sticky_ttl_sec": 600,
  "auth_required": false,
  "username": "",
  "password": "",
  "allowed_cidrs": [],
  "rate_limit_bps": 0,
  "max_parallel_dial": 1
}
```

`status.chain_options` 回显当前生效值；`chain_path` 为路径文案（本机 → 入口 → … → 目标）。

---

## 设置 / Token / 审计

| 方法 | 路径 | 说明 |
|------|------|------|
| GET/PUT | `/api/settings` | 面板与运行参数；`feature` / `feature_json` 含卡片、链式、Webhook 等 |
| GET/POST | `/api/tokens` | API Token 列表 / 创建（明文仅创建时返回一次） |
| DELETE | `/api/tokens/{id}` | 删除 Token |
| GET | `/api/audit?page&size&action` | 操作审计 |
| POST | `/api/system/restart` | 重启 |
| GET | `/api/events` | SSE |
| GET | `/api/mihomo/*` | Mihomo 状态/安装 |

### settings.feature 常用键

- `dashboard_cards`: `{ "available": true, "chain": true, … }`
- `chain`: 同上链式对象
- `direct_sticky_enabled`, `webhook_url`, `alert_validated_min`, `allowed_cidrs`, …
- `free_validate_urls`: 多校验 URL 数组

---

## 订阅 / 节点 / 池

与 Super-Proxy-Pool 兼容：

- `/api/subscriptions*`
- `/api/manual-nodes*`
- `/api/pools*`（策略模板、free_proxy 成员、发布）

---

## 前端

页面统一通过 `frontend/src/api.ts` 的 `endpoints` 调用，避免散落硬编码路径。
