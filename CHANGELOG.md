## Unreleased — 2026-09-10 · 链式失败归因与前置代理

接着上一轮审计，把链式代理（`:7893`）与前置代理（`exit_via`）里三类会自己扩散的问题修掉。
**行为有变化的地方标了「影响」。**

### 安全
- **`exit_via` 配坏时改为拒绝拨号（fail-closed）**：过去 `withVia` 解析失败就返回原始 hop 列表，
  于是 `exit_via` 里一个拼写错误就让流量直接从免费代理出网，而面板照旧显示
  「本机 → VPS → …」—— 相当于 VPN 隧道掉线却没有断网开关，用户拿不到任何信号。
  现在解析失败或解析出空地址就返回错误，单跳与链式两条路径都不再拨号。
  *影响：`exit_via` 写错不再静默降级为直连，而是整个出口不可用并报错；配置里的拼写错误会立刻暴露。*
- **`https://` 不再被偷偷改写成 `http://`**：作为**代理协议**的 `https://` 意思是「到代理这一跳本身裹一层 TLS」，
  和「HTTP 代理用 CONNECT 承载 https 流量」是两回事，本仓库没有任何代码实现前者
  —— `dialFast` 开的是裸 TCP，`httpConnectOver` 写的是明文 CONNECT。
  原来的 `proto = "http"` 让显式要求 TLS 到自己 VPS 的用户拿到明文通道，账号密码一起走明文。
  现在直接报错并提示改用 `http://` 或 `socks5://`（隧道内部无论哪种都是加密的）。
  *影响：配了 `exit_via: https://…` 的部署会启动即报错，需要改成 `http://` 或 `socks5://`。*

### 代理池质量
- **链式失败按真正断掉的那一跳扣分**：`dialProxyChainPool` 以前把每一层失败都包成普通 error，
  调用方无从区分「第 0 跳挂了」和「第 2 跳挂了」，于是 `dialChainWithFailover` 一律扣 `hops[0]`：
  健康的入口代理被反复扣分直到删除，真正坏掉的那一跳保持 `ScoreMax` 留在轮换里，
  下一次继续把链子搞断。新增 `chainDialError` 记录出错的 hop，`culpritHop` 供调用方查元凶。
- **前置代理挂掉不再把整个池子洗空**：配了 `exit_via` 时 `wired` 是 `[VPS, up]`，
  VPS 一死则每次尝试都失败，而失败全记在 `up` 头上 —— 每个请求淘汰一条好代理，
  池子按请求速率被清空。现在只有 `up` 自己是元凶时才 `MarkValidated(false)`。
- **`exit_via` 这一跳永不参与打分**：它是用户配置而非池成员，按 `Source == "exit_via"` 跳过。
- **`socks4://` 前置代理真正可用**：`viaPool.dialReady` 原先把 `socks4` 和 `socks5`
  归到同一支，向 SOCKS4 端点发 SOCKS5 方法协商帧会直接把它打乱序 —— 经 socks4 前置的连接
  必然死在第一跳。SOCKS4 没有独立握手（CONNECT 请求本身就是线上第一个包），
  现在交回裸连接由 `tunnelThrough` 分派到 `socks4ConnectOver`；
  也不能包 `socksAuthed`，否则 socks5 仍然需要的那次握手会被跳过。
  （`dialReady` 里的 `socks4a` 分支目前不可达：`viaPool` 只由 `ParseViaProxy` 构造，
  而它不接受 `socks4a://`。留着是为了以后放开该 scheme 时不再退回 socks5 分支。）

### 测试
- 三个回归测试：只支持正向 GET、不支持 CONNECT 的代理不得进链；
  `https://` 前置协议被拒；`exit_via` 配坏时触发 fail-closed 而非绕过。

### 已知缺口
- 链式选路阶段仍未单独探测 **CONNECT 能力**：目前靠校验器保证，但 report API 路径可能绕过。
  实测样本 `218.252.100.222:80` 说明「校验通过」不等于「能当链式中继」。
  要彻底堵上需在 `filterCandidates` 或链式选路里加一次 CONNECT 探测（多一个网络往返）。

## Unreleased — 2026-09-09 · 安全与正确性修复

一轮全面审计后的集中修复。**行为有变化的地方在每条末尾标了「影响」。**

### 安全
- **热更新不再可被池子里的恶意出口投毒**：回退通道改用校验证书的客户端（原先复用采集器的
  `InsecureSkipVerify` 客户端，且出口就是免费代理池，等于把任意 ELF 交给对方）；
  新增 `unified-proxy-pool.sha256` 校验，校验和只从验证过证书的通道取，取不到就拒绝升级。
  *影响：CI 必须随二进制发布 `.sha256`（已加）。*
- **`/api/public/report` 不再采信调用方的结论**：过去无鉴权、无限速地把任意地址置为
  `Validated + ScoreMax`，局域网内任何人（甚至网页里的一个简单 POST）都能把自己的出口塞进池子。
  现在只登记线索、由面板自己复测，并按 IP 限速。*影响：报告结果不再立即生效。*
- **链式代理 `:7893` 的认证 / CIDR / 限速真正生效**：面板一直在收集
  `chain.auth_required`、`username`、`allowed_cidrs`、`rate_limit_bps`，但代码从未读过，
  勾了也等于没勾。同时补上限速的实际实现（令牌桶）。
- **未设账号密码的监听不再对公网开放**：`allowed_cidrs` 为空时隐式只允许私网/回环，
  并在启动日志里提示。*影响：从公网 IP 直连 7892/7893 会被拒，需显式配 CIDR 或账号密码。*
- **API Token 范围真正生效**：`proxies:read` / `proxies:write` / `channels:write` / `ai:write` / `admin`，
  写操作与 AI 接口逐路由校验。*影响：过去用只读 Token 调 submit 的脚本要换成 `proxies:write`。*
- **转发头默认不再被信任**：新增 `feature.trusted_proxy_cidrs`，为空时忽略
  `X-Forwarded-For` / `X-Real-IP`；配置后按最右侧非可信条目取客户端 IP。
  *影响：装在 nginx 后面的部署要填这一项，否则审计日志与 LAN 判定看到的是反代地址。*
- 出口国家探测改用 HTTPS 且验证证书 —— 明文 HTTP 走被测代理时，对方可以直接伪造国家绕过 CN 封禁。
- **存活校验不再只看状态码**：`InsecureSkipVerify` 让 HTTPS 判定 URL 也能被 MITM 的代理伪造，
  校验证书后它就伪造不了；判定 URL 是明文时，额外要求通过一个证书校验过的
  HTTPS 金丝雀（`cp.cloudflare.com/generate_204`），证明它真的在转发。
  实测抽样：只会应答常见探针的假代理 40/40 倒在金丝雀上，真代理 29/40 通过；
  全量 72,340 条跑下来，"可用"从 947 条收敛到百余条。
  *影响：只对判定 URL 有反应的假代理会被判死，池子会变小但都是真的。*
- 登录限速（每 IP 5 分钟 10 次）、密码至少 6 位、代理密码常量时间比较、
  SOCKS5 目标域名校验（防 CR/LF 注入到上游 CONNECT）、响应体大小上限、分页上限、
  安全响应头（`X-Frame-Options`/`nosniff`/CSP）。
- 采集到的私网/回环地址一律丢弃；提交接口只拒绝面板自己的监听端口（本地 clash 仍可入池）。
- 提交的 `validated`/`score`/`latency` 等字段不再采信，一律按未测处理。

### 稳定性
- **mihomo 配置被拒不再引发崩溃循环**：4xx（配置被拒）时保留上一份可用配置并报错，
  只有传输层失败才重启。此前一个订阅里的坏节点就能让所有出口反复重启。
- **发布配置串行化 + 唯一临时文件**：并发 Publish 曾在固定的 `.tmp` 上互相踩踏，
  导致 rename 失败甚至半截 YAML。配置文件权限收紧到 0600（内含控制口令与节点密码）。
- **`crawlers.Registry` 加锁**：面板增删采集源与调度器遍历并发时会触发
  `concurrent map read and map write`，整个进程直接挂掉。
- mihomo 子进程设 `Pdeathsig`：父进程 `log.Fatal` 或 exec 后不再留下抢端口的孤儿进程。
- 监听 accept 出错改为退避重试（此前一次 EMFILE 就会让监听永久停摆）。
- 明文 HTTP 转发路径补上游超时；响应中继用独立预算，不再被 30s 连接级 deadline 掐断。
- 面板 HTTP 服务补 `ReadTimeout`/`IdleTimeout`（SSE 不设 WriteTimeout）。

### 代理池质量
- **Redis 排序修复**：`MarkValidated` 永远写 `ScoreMax`，导致所有按分数的 ZSET 区间退化成
  地址字典序 —— 复检永远只测同一批、`RandomN` 永远只发同一批、`Trim` 淘汰的是刚验过的。
  新增按最后检查时间排序的集合，`RandomN` 改用 `ZRANDMEMBER`。
- **多校验 URL 只写一次结论**：过去每个 URL 各写一次，URL1 失败先把 raw 代理删掉，
  URL2 再以 `http`/`manual` 重建，协议与来源全丢；两个 URL 失败就够删掉一条活代理。
- 上下文取消/超时不再计为代理失败（此前批次超时会删掉一整批未测代理并惩罚其来源）。
- 存活判定收紧为 2xx —— 此前 407/403 也算“可用”。
- **SOCKS4 真正可用**：新增 SOCKS4/4a 拨号（校验与链式出口都接上），
  此前 socks4 按 SOCKS5 握手必然失败，十几个默认开启的 socks4 源纯占坑。
- 认证代理（手动节点）校验时带上账号密码，不再一律判为不可用。
- 出口池成员为空时用 `REJECT` 而非 `DIRECT` —— 后者会静默改用本机 IP 出网。
- 自动调优不再擅自开关采集源（此前被手动关掉的源会被自己打开）。

### 节点导入
- **分享链接的 TLS/传输参数不再丢失**：vmess 的 `net/tls/host/path/aid/scy`、
  vless/trojan 的 `security/sni/fp/pbk/sid/serviceName` 现在会翻译成 mihomo 的
  `network/tls/servername/client-fingerprint/reality-opts/ws-opts/grpc-opts`。
  此前这些节点被当作明文 TCP 发布，Reality 节点导入后完全不可用。
- SIP002 形式 `ss://…@host:port/?plugin=…` 不再因为路径段解析失败被丢弃；
  `plugin` 转成 `plugin`+`plugin-opts`（缺 `mode` 的 obfs 会去掉插件而不是让整份配置 fatal）。

### 数据与依赖
- 内存+SQLite 模式：自定义分组与来源产出纳入快照（此前重启即丢），
  快照写入从 3s 放宽到 20s（关闭时仍立即落盘），来源统计改为单事务批量写。
- 订阅代理拉取复用 Transport（此前每次同步泄漏一批空闲连接）。
- Go 工具链 1.25.0 → 1.25.13，`golang.org/x/net` 0.53.0 → 0.55.0：
  govulncheck 从 32 个可达漏洞降到 0。
- 面板可编辑的 AI 提示词真正生效（此前保存了但调用时仍用内置模板，自定义 key 还会丢掉正文）。

## Unreleased — 2026-08-07

### CF 优选扫描（Free-Fly）
- 面板「CF 优选」：粘贴 IPv4/CIDR（上限 2 万），先扫 443 再 TLS 探 `/cdn-cgi/trace`（SNI `speed.cloudflare.com`）。
- 命中存 SQLite，可导出 `cf_proxy_ips.txt`，或套到已有 vless/trojan/vmess 手动节点（只改 server）。
- **不会**写入 7892 免费 HTTP/SOCKS 池。
- 「填入官方 /24 抽样」：从 Cloudflare 公布的 IPv4 段里抽 32 个 /24（约 8k IP）做边缘优选，不必自己找段。

### 热更新
- 设置页可检查 / 一键热更新：CI 每次 main 推送把 linux/amd64 二进制挂到 GitHub Release `nightly`，运行中的进程下载后 `exec` 替换自己（Docker 里也不用 rebuild）。
- `/api/public/debug` 带 `version.commit`，能看出软路由跑的是哪一版。
- `GET/POST /api/system/update`，`GET /api/system/version`。下载失败会试 ghproxy 和已发布 mihomo 池。

### 采集源出网
- GitHub 列表不再走 jsDelivr/Cloudflare（`104.17.x:443` 在国内 WAN/Docker 桥上黑洞）。默认镜子：`cdn.jsdmirror.com`（国内 IP）→ ghproxy.net → GitHub raw。
- 采集失败会把**每一面镜子**的错误拼进 `last_error`，不再只留最后一条。
- 进程内一旦 jsDelivr 超时，后续源直接跳过，不再每个源空等 8 秒。
- 设置「采集出网代理」：空=先直连（jsdmirror 不绕池），网络/TLS 失败再走已发布 mihomo 池 `127.0.0.1:3000x`；`none` 只直连；也可填 `chain`/`7893`/`socks5://…`。
- 采集单 URL 超时 15s（TLS 握手 8s），避免握手时限比请求上下文还长。

### 死代理不再出站
- `/api/public/get`、7892/7893、出口池**只拿已验证**；未测 raw / 重试队列不再当可用代理发出去。
- 未测失败当场删除（不再堆几千条重试僵尸）；曾经活过的失败进重试，再失败就删。
- 校验页「清空死代理」：`POST /api/proxies/purge {"dead":true}` 清掉整个 retry 集合。

### 校验生命周期
- 测完即离开 raw（4000 只装**未测**），别的源可以陆续补上。
- 连通 → 已验证维护清单（上限 2000）；未测不通直接丢；曾经活过的再给一次 5 分钟重试。
- 校验批次：未测优先，夹一点到期重试和已验证复检。

### 校验批次
- 每轮不再固定抽 Redis 分数最低的同一批 ~140：raw 按**从未测过 → 最久未测**排队，自动分批把全部节点扫完。
- 调度器一次触发会连续多批（7 分钟预算），未扫完则约 30s 后续上，不必干等到校验周期。
- 复检名额按实际已校验数量给，空着的还给 raw。单批默认 400。
- 校验页增加「全量进度」：未测数 / raw 总数、大约还要几批。

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
