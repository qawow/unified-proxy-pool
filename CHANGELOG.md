## Unreleased — 2026-08-07

### 新功能
- 采集源页面已支持删除自定义源
- 出口池新增**按类型一键选择**（免费代理 / 订阅 / 手动节点），链式代理等也从出口池选择成员发布

### 文档更新
- README.md 优化启动、Docker 和功能描述

## 0.2.0 — 2026-08-04

### 链式代理（原「代理套代理」）
- 全站命名统一为 **链式代理**；路径展示：本机 → 入口 → [中继] → 出口 → 目标
- 面板可配置：启用、监听、跳数 2–4、容错次数、总/单跳超时、去重 Host/地区、入口/出口协议与地区、粘性、认证、CIDR、限速、并行拨号
- `PUT /api/direct-proxy/chain` 支持完整 `chain_options`（兼容仅 `hops`）
- 客户端脚本：`/api/direct-proxy/client-pack?mode=chain`

### 性能
- Redis meta **MGET/pipeline**；`RandomN` 缩小窗口
- 已验证列表 **ZSET 真分页**；RegionTop/Queues 采样 + 批量读
- **HotCache**（约 3s）供单跳/链式选路，降低 Redis 读放大
- Overview **3s 缓存**；AddRaw 批量存在性检查

### 面板与运维
- 仪表盘：实时连接 **入站/出站**；卡片在设置中可开关
- 代理池：完整连接串复制、行详情、导出 txt/URL、清理未验证、拉黑
- 校验统计：**实时简易日志**（开始/单条/结束）
- 系统设置：分组（面板/探测/调度/链式/高级 feature）

### API / 安全 / 可观测
- 黑名单 CRUD；代理 export / purge
- API Token；审计日志；公开 health 增强；`POST /api/public/report`
- `/metrics`；流量 history；connections；health-board；Webhook 配置位

### 其它
- 登录会话可配置时长（默认 7 天，SQLite 持久化）
- 校验结果 GeoIP；调度间隔可设置热读

## 0.1.0 — 初始

- Super-Proxy-Pool 骨架 + 免费代理管道 + DirectProxy 单跳/多跳基础
- React 面板嵌入、Docker Compose、公开 get/count API
