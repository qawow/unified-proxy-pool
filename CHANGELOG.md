## Unreleased — 2026-08-07

### 采集源出网
- GitHub 列表不再走 jsDelivr/Cloudflare（`104.17.x:443` 在国内 WAN/Docker 桥上黑洞，一条 `i/o timeout` 会显示成整个源挂了）。默认镜子：ghproxy.net → GitHub raw。
- 采集失败会把**每一面镜子**的错误拼进 `last_error`，不再只留最后一条。
- 进程内一旦 jsDelivr 超时，后续源直接跳过，不再每个源空等 8 秒。
- 设置「采集出网代理」：`chain`/`7893`、`direct`/`7892`、`socks5://…`，给软路由容器一条能出墙的路。

### 校验批次
- 每轮不再固定抽 Redis 分数最低的同一批 ~140：raw 队列按随机窗口 + 10 分钟冷却跳过刚测过的，点第二次会换一批。
- 复检名额按实际已校验数量给，空着的还给 raw（以前 200×70%=140 raw，已校验只有 2 个时仍只测 143）。
- 手动/定时一轮默认 400（raw 最多约 400，而不是 140）。

### 校验统计 / 采集源自愈 / 检测
- 来源成功率改看**最近窗口**（50 次），终身失败不再把源永久关死；禁用 TTL 不再每轮续期，到期后窗口恢复则自动解禁，仍差则 1h→2h→4h 退避。
- 校验页展示近窗通过率，自动停用可一键恢复；CN 拦截记 `skip`，不计入源失败。
- 每轮校验写入 sourceyield 历史（单源抽样 <20 丢弃，避免和 CLI 60 样本混用）；调度器只**自动开回**已恢复的源，不自动关采集（关采集会断恢复样本）。选路侧停用仍由 sourcestats 负责。
- 延迟/测速 sweep 跳过 CN 与 sanitize 毒节点。

### 永久屏蔽 CN + 真实出口国家
- 默认 `blocked_countries=["CN"]`（大陆；HK/TW/MO 保留，设置里可加）。空列表=不屏蔽；关掉「启用国家屏蔽」才放行 CN。
- 三层检测：采集 JSON 的 country 字段（geonode 等以前丢掉了）、主机 GeoIP（ip-api + ipwho.is）、校验时**经代理**请求 ip-api 得到网站看到的出口国家（monosans/ProxyBroker 做法）。
- 入库、选路、校验、mihomo 发布、订阅同步、手动节点全部拦截；保存设置时删除池里和订阅/手动里已有的匹配项。
- 「中国」「大陆」节点名、`.cn` 主机名视为 CN；「美国 CN2」不算。

### 渠道封禁（按目标站点的临时禁用）
- 新增「渠道」概念：渠道 = 请求的目标站点。某个代理 IP 在淘宝被限流，只在淘宝渠道上临时撤下，其它站点照常使用；到期自动解封
- 封禁规则可配：命中状态码（默认 403/429）即时封、连续失败、窗口内失败率、窗口内超时次数；阈值填 0 关闭对应规则
- 反复触发指数退避（60s → 120s → 240s …），到 `ban_ttl_max_sec` 为止
- 明文 HTTP 的 403/429 由代理池**自动**识别并记账；HTTPS 走 CONNECT 隧道看不到状态码，需调用方通过 `POST /api/channels/report`（或免鉴权的 `/api/public/channels/report`）回传，支持单条、`target` 自动推导渠道名、以及单次最多 500 条的批量
- 取代理支持 `?channel=` / `?target=`：该渠道封禁中的 IP 不会返回。若某渠道把所有可用代理都禁了，则忽略封禁兜底返回并在 JSON 中标记 `"relaxed": true`——给个可能被限流的代理胜过给个 502
- 新增面板页「渠道封禁」：按渠道看成功/失败/超时/失败率、封禁明细与剩余时间、手动解封 / 重置 / 删除；封禁与解封通过 SSE 实时推送
- 阈值与选路策略在「系统设置 → 渠道策略与选路」，保存即时生效
- 封禁持久化到 SQLite，重启恢复未到期部分；滑动窗口计数不持久化，重启后重新观察，不拿旧证据封人
- 新增指标 `upp_channels_total`、`upp_channel_bans_active`（仅聚合值，渠道名会导致标签基数爆炸）
- 新增事件 `channel_ban` / `channel_unban`（SSE + Webhook）
- 渠道数与每渠道跟踪 IP 数均有上限（默认 500 / 2000），超限按最久未活跃淘汰
- 请求日志：内存环形缓冲记录每次自动观测/调用方上报的结果，触发封禁的那一行会标出来；面板「渠道封禁」页可看、可按渠道过滤、可清空。重启即丢，封禁本身不受影响
- 请求日志同时落 SQLite（`channel_outcomes`），默认保留 48 小时，重启后恢复最近 500 条；到期由 sweeper 清理
- 到期复检：TTL 过后默认不立刻放回，等到该 IP 在该渠道上再次成功才真正解封，避免「放出 → 立刻再封」
- 白名单：`POST /api/channels/allowlist`，按渠道或全局保护某个 IP 不被自动禁；面板封禁明细里有「永不自动禁」
- 公开上报 `POST /api/public/channels/report` 按来源 IP 限流（每秒 50 条），防误触把池子禁光
- 粘性会话真正生效：把客户端 IP 传入选路；记住协议，不再把 SOCKS5 粘成 HTTP CONNECT；已被该渠道禁用的粘性 IP 会丢掉
- 实时连接：`conntrack.Begin/End` 接到 DirectProxy，仪表盘连接数和 `/api/stats/connections` 不再永远是 0
- Webhook 默认事件加上 `channel_ban`；仪表盘新增「渠道封禁」卡片
- AI 搜索思考等级改为各家通用的 `off` / `low` / `medium` / `high` / `max`，请求带 `reasoning_effort`；旧的 0–10 `level` 仍映射有效

### 选路策略
- 取代理由「取回来的第一个」改为可配策略：`weighted`（默认，按评分/延迟/失败次数加权随机）、`p2c`（Resin 式二选一，更稳更省）、`random`（等概率）、`rr`（按渠道各自轮转，渠道间互不干扰）
- 加权是降权而非排除：差的代理概率低但仍有机会，否则它永远没机会证明自己已恢复
- 新增重复取用冷却（默认 30s）：刚发出去的代理短时间内降权，避免请求全挤在同一个 IP 上
- `?count=N` 一次取多条且不重复（上限 100）；纯文本每行一条，JSON 为 `items` 数组。不带 `count` 时响应结构与旧版完全一致

### 修复
- **局域网调试接口 + 防护**：`/api/public/*`（取代理、入池、health、`/debug`）默认只允许 RFC1918/环回；公网 403。`X-Forwarded-For` 仅本机反代可信。入池另有每 IP 20 次/秒。设置可加 `allowed_cidrs` 或危险项 `public_open`。调试：`GET /api/public/debug` 看 7892/7893 与 mihomo probe 是否在跑、上次异常退出原因。
- 回归锁：`TestSanitizeProxyMapProductionFatals` + `TestProbeYAMLNeverContainsMihomoFatals` 覆盖 SS 乱码 cipher、alpn 字符串、vless `none=`、`tls` 空字符串、缺 uuid 的 vmess；订阅字段末尾 `=` 会先剥掉再校验。
- 全量清洗：未知 `type`、缺 uuid/password、`port` 字符串、`ws-opts` 等本应是 map 的字段写成字符串、非法 `network`/`client-fingerprint`，一律改掉或跳过，避免下一条「expected type」再打死 probe。
- **tls 字符串 / 残缺 vmess 不再打死 probe**：`tls: ""` 会 `'tls' expected type 'bool'`；缺 `uuid/alterId/cipher` 的 vmess 会 `has unset fields`。布尔字段收成 true/false，vmess 补 `alterId=0`/`cipher=auto`，没有 uuid 的直接跳过。
- **一条坏 vless 不再打死 mihomo probe**：`encryption: none=`（线上 id 104004 / `85.133.215.108:235`）会 `invaild vless encryption value: none=` 然后 probe 退出。发布前把 `none=` 收成 `none`，其它非法值跳过。
- **一条坏 SS 节点不再打死 mihomo probe**：订阅里 `cipher` 乱码（线上：`dash.zendegizibast.ir:2087` → `unknown method: �G`）或 `alpn` 写成字符串（`'alpn' is not a slice`）时，mihomo 解析整份 probe YAML 失败并退出，面板 7891 仍 200、7893/测速抖动。发布前跳过无法初始化的节点，并把 `alpn` 收成列表。复现：池子留一条 `"cipher":"�G"` 的 ss 再 publish，旧镜像会刷 `mihomo probe exited`；修好后同样数据 probe 不再退出。
- 修复请求指定协议（如 `?proto=socks5`）时，HotCache 在缓存内无匹配协议时会回退返回**其它协议**的代理，且下游不再校验协议、导致池中真有 socks5 代理却被静默换成 http 的问题。协议校验下移到统一的候选过滤入口，仅在池中确实没有该协议时才按既有降级阶梯放宽

### 新功能
- 本机→VPS 第一跳：预热 SOCKS 连接池（握手复用）、TCP_NODELAY、KeepAlive；`/api/public/debug` 的 `vps_via` 看命中率。
- 订阅拉取：URL-safe Base64、HTML 拦截、8MB 上限、502/429 重试、SOCKS5 获取代理真正走 SOCKS、同步结果区分新增/更新/删除；超长 URI 不再被截断。延迟测试对无法进 mihomo 的节点给出 skip 原因。
- 订阅头识别 Cloudflare Workers/Pages（`workers.dev` / `pages.dev` / edgetunnel）自动用 clash.meta UA
- 采集源增加 `spys-socks`（默认关）；Docker 内核默认 mihomo `v1.19.30`
- LAN debug 返回本机是否具备全局 IPv6（HE 隧道/原生前缀）
- 采集源页面已支持删除自定义源
- 出口池新增**按类型一键选择**（免费代理 / 订阅 / 手动节点），链式代理等也从出口池选择成员发布
- 新增 AI / 脚本代理批量入池 API 与面板，支持 JSON、纯文本和独立来源标记
- AI 爬取面板新增「AI 搜索」：可填任意 OpenAI 兼容接口（URL + API Key + 模型）、0–10 思考等级，按提示词把网页内容/关键词转成代理候选并一键并入入池
- 内置三套可修改提示词模板（提取列表 / 分析线索 / 生成候选），面板可编辑、可恢复默认，`GET/PUT/DELETE /api/ai-prompts` 管理

### 修复与完善
- 修复 `/api/ai-proxy` 文档声明支持 Bearer Token、实际却仅接受登录会话的问题
- AI 入池严格执行 1 MB 请求上限，补充对象数组解析、协议归一化及重复/拒绝/净增长反馈
- 完善 AI 入池页面的输入预览、容量提示和提交结果展示

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
