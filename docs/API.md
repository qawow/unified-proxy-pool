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
| GET | `/api/public/get?format=json&proto=&region=&family=` | JSON；可筛协议/地区/IP 家族 |
| GET | `/api/public/proxies/random?proto=&region=&family=` | 随机可用代理 JSON |
| GET | `/api/public/proxies/count` | `{ total, validated, raw, count }` |
| GET | `/api/public/count` | 同 count |
| POST | `/api/public/report` | Body `{ "addr", "ok", "latency_ms?" }` 质量反馈 |
| POST | `/api/public/submit` | 纯文本，每行一条 `host:port`，批量入池（见下） |
| GET | `/metrics` | Prometheus 文本指标 |

```bash
curl http://172.18.49.135:7891/api/public/get
curl 'http://172.18.49.135:7891/api/public/get?format=json&region=US'
# 只要 IPv6 出口
curl 'http://172.18.49.135:7891/api/public/get?format=json&family=ipv6'
curl http://172.18.49.135:7891/metrics

# 脚本抓完直接推进池子（免鉴权，仅限内网）
printf '1.2.3.4:8080\nsocks5://5.6.7.8:1080\n' \
  | curl -sS -X POST 'http://172.18.49.135:7891/api/public/submit?source=my-crawler' \
         -H 'Content-Type: text/plain' --data-binary @-
```

`/api/public/submit` 免鉴权是给内网脚本用的（512KB 上限）。要鉴权版本和批量验活，用下面的 `/api/proxies/submit`。

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
| GET | `/api/proxies?page&size&q&proto&source&only_ok&region&family&group` | 分页列表 |
| GET | `/api/proxies/export?format=txt\|url\|json&…&family&group` | 按筛选导出 |
| POST | `/api/proxies/purge` | 条件清理，见下 |
| GET | `/api/proxies/random?proto=&region=&family=` | 随机 |
| GET | `/api/proxies/count` | 计数 |
| POST | `/api/proxies/test` | `{"addr":"1.2.3.4:8080"}` |
| DELETE | `/api/proxies?proxy=1.2.3.4:8080` | 删除 |
| POST | `/api/proxies/submit` | 批量入池，接受 session cookie 或 Bearer token |
| POST | `/api/proxies/batch-test` | 批量验活（一次最多 200 条） |

### 脚本入池：submit / batch-test

这两个接口是给「脚本抓完，要把结果交给池子」这条链路用的。跟 `cmd/scanproxies` 的区别是不需要 Redis 权限，能从别的机器上跑。

**鉴权**：这两个接口同时接受 session cookie 和 `Authorization: Bearer upp_…`。脚本用 token —— 脚本存不住 cookie。token 在设置页生成，或 `POST /api/tokens`。

```bash
# JSON 形式
curl -sS -X POST http://127.0.0.1:7891/api/proxies/submit \
  -H "Authorization: Bearer ${UPP_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"proxies":[{"host":"1.2.3.4","port":8080,"protocol":"http"}],"source":"my-crawler"}'

# 纯文本形式（每行一条，支持 scheme 和 userinfo，会被剥掉）
printf '1.2.3.4:8080\nsocks5://user:pass@5.6.7.8:1080\n[2001:db8::1]:1080\n' \
  | curl -sS -X POST 'http://127.0.0.1:7891/api/proxies/submit?source=my-crawler' \
         -H "Authorization: Bearer ${UPP_TOKEN}" \
         -H 'Content-Type: text/plain' --data-binary @-

# 批量验活
curl -sS -X POST http://127.0.0.1:7891/api/proxies/batch-test \
  -H "Authorization: Bearer ${UPP_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"addrs":["1.2.3.4:8080","5.6.7.8:1080"],"concurrency":20,"timeout_ms":8000}'
```

**submit 的返回不止 `added`**：

```json
{
  "submitted": 5, "parsed": 4, "added": 4, "duplicates": 0,
  "net_growth": 1, "evicted": 3, "raw_at_cap": true,
  "note": "raw pool is at its cap (4000), so 3 existing proxy/proxies were evicted…"
}
```

`added` 单独看会骗人。原始池到 `MaxRawProxies`（4000）之后，塞进 N 条就会让 `Trim` 淘汰 N 条，于是「added 4」而池子只涨 1 条 —— 实测就是这个数。所以返回里带 `net_growth`（池子实际涨了多少）和 `evicted`（为了腾位置淘汰了多少），`raw_at_cap` 说明这是容量压力不是 bug。**脚本要看 `net_growth`，不是 `added`。**

淘汰的是分数最低的，可能是别的源的代理。真想扩容得先 purge 掉不通的，或者调高上限。

**batch-test 一次最多 200 条**（`Service.BatchTest` 的 `maxItems`），超了会被静默截断 —— 所以 `submit-proxies.sh` 主动按 200 切。返回里 `ok + fail` 小于送进去的条数就是被截断了。

验活会真的改分数：活的进 `validated`，死的按面板规则扣分或删掉，跟定时校验轮一致。

### 列表返回与 `truncated`

```json
{ "items": [ ... ], "total": 42, "page": 1, "size": 20, "truncated": false }
```

带筛选条件（`family` / `group` / `source` / `q` 等）时，Redis 后端只扫描一个有上限的窗口（默认 800 条，导出路径 5000），因此：

- `total` 是**窗口内实际匹配数**，据此分页不会出现空页
- `truncated: true` 表示窗口已扫满，窗口之外可能还有匹配项，此时 `total` 是下界（前端显示为 `42+`）；缩小筛选范围可得到精确值
- 无筛选条件时走 Redis 原生分页，`total` 为精确值，不会出现 `truncated`

### IP 家族（IPv4 / IPv6）

每条代理都带 `ip_family` 字段，取值 `ipv4` / `ipv6` / `unknown`（域名）。入库时自动按 `host` 判定，历史数据读取时惰性推导。

`family` 查询参数用于筛选，接受这些别名：

| 传入 | 归一化为 |
|------|----------|
| `ipv4` / `v4` / `4` / `inet` / `inet4` | `ipv4` |
| `ipv6` / `v6` / `6` / `inet6` | `ipv6` |
| `unknown` / `host` / `hostname` | `unknown` |
| 其它（含空值） | 忽略该筛选 |

IPv6 地址在 `addr` 中始终带方括号，如 `[2001:db8::1]:1080`。

### 分组

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/proxies/groups` | 列出内置 + 自定义分组，含实时计数 |
| POST | `/api/proxies/groups` | 新建/覆盖自定义分组 |
| PUT | `/api/proxies/groups/{name}` | 更新指定分组（路径名优先） |
| DELETE | `/api/proxies/groups/{name}` | 删除自定义分组 |

内置分组（不可编辑/删除）：`ipv4`、`ipv6`、`http`、`socks5`、`validated`。

创建/更新请求体：

```json
{
  "name": "fast-v6",
  "label": "快速 IPv6",
  "sources": ["alpha"],
  "protocols": ["http", "socks5"],
  "families": ["ipv6"],
  "regions": ["US", "JP"],
  "min_score": 30,
  "only_ok": true
}
```

规则语义：同一维度内多个值取「或」，不同维度之间取「与」；留空表示该维度不限制。规则不能全为空。分组名限 `[a-zA-Z0-9][a-zA-Z0-9_-]{0,39}`，保存时转小写；内置名不可占用。

列表返回项在分组字段外附加 `total` 与 `validated` 计数。用 `/api/proxies?group=fast-v6` 按分组筛选，可与 `family` 等其它参数叠加（同时生效，取交集）。

`/api/validator/queues` 的返回中新增 `family_counts`，按家族统计已验证代理数。

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

### 两个 `last_ok` 不是一回事

- `/api/scrapers` 的 `last_ok`：上一轮**采集**新入库的条数
- `/api/scrapers/stats` 的 `ok` / `fail`：该来源代理在**校验**中的成功/失败次数（来自 `sourcestats`）

同名不同义。一个源可以采集 `last_ok=28633` 而 stats 里 `ok=0`——后者要等校验轮跑过才有数。

### 自定义源的 `format`

| format | 适用 |
|--------|------|
| `plaintext` | 正文直接是 `1.2.3.4:8080`，也认 `[2001:db8::1]:1080` |
| `json` / `jsonl` | JSON 数组、`{"data":[…]}` 包装对象、或每行一个 JSON；**host 与 port 分开写也能读** |
| `html_table` | 从 `<table>` 取，配 `host_col` / `port_col` |
| `html_regex` / `socks_list` | 按正则从 HTML/文本里提取 |

`json` / `jsonl` 会尝试这些字段别名：host 取 `ip`/`host`/`hostname`/`server`/`address`，port 取 `port`（数字或字符串都行），已拼好的地址取 `proxy`/`addr`/`ip_port`/`endpoint`/`url`，协议取 `protocol`/`type`/`scheme` 或 `protocols[0]`。解析不出 JSON 时自动回退到正则提取。

---

## 命令行工具

不依赖运行中的面板，直接复用面板里的同一套 crawler / 校验代码：

| 命令 | 用途 |
|------|------|
| `go run ./cmd/sources` | 打印内置源清单（`-enabled` / `-format` / `-proto` / `-urls`） |
| `go run ./cmd/fetchproxies` | 按源抓取并去重（`-name` / `-family` / `-stats` / `-out`） |
| `go run ./cmd/checkproxies` | 验活一份清单，按延迟排序输出 |
| `go run ./cmd/discover` | 批量探测候选源，判定该不该加（见下） |
| `go run ./cmd/scanproxies` | 扫代理并直接写进 Redis（见下），默认试跑 |
| `go run ./cmd/testsource` | 用真解析器试解析一段响应体（供 add-source.sh 调用） |

配套脚本在 `scripts/`：

```bash
./scripts/check-sources.sh --stats          # 源健康：ok / SILENT / FAILED
./scripts/discover-sources.sh --emit-go     # 找新源，输出可粘贴的 Go 声明
./scripts/fetch-proxies.sh --check          # 抓取 + 验活 → out/proxies-<时间>.txt
./scripts/scan-to-pool.sh --write           # 扫 + 验活 + 直接写进池子
./scripts/import-proxies.sh --in live.txt   # 经面板接口导入（少量）
./scripts/add-source.sh --name x --url … --test-only   # 试解析后再注册
```

`check-sources.sh` 区分三种状态，**SILENT 最值得追**：请求成功但解析出 0 条，说明格式变了或解析器读不了这个源——在面板里它和「源本来就没数据」长得一样。

`fetch-proxies.sh --check` 会实测每条，免费 http 源存活率通常 1%～5%，所以先筛后导入，别把几万条原始地址直接灌进面板。

### 发现新源：看 ADDS，别看 NEW

候选清单放在 `scripts/source-candidates.txt`（一行一个 URL，已在 `sources.go` 里的自动跳过）。`discover` 对每个候选回答三个问题：

| 列 | 含义 |
|----|------|
| `VERDICT` | `KEEP` / `DEAD` / `EMPTY` / `UNPARSED` / `DUPLICATE` / `REDUNDANT` / `MOSTLY-DUP` / `TOO-SMALL` |
| `FORMAT` | 逐个格式试解析后产出最多的那个（不靠 URL 后缀猜） |
| `NEW` | 相对基线的新增数——**每个候选单独算** |
| `ADDS` | 扣掉排名更高的候选已覆盖的部分之后，真正的增量 |

**ADDS 才是决策依据。** 这些 GitHub 清单互相抄得厉害：实测 `SoliSpirit/proxy-list`（122777 条）与 `MuRongPIG/Proxy-Master`（101628 条），后者地址**全部**已包含在前者里。两个单独跟基线比都是「新增 7.4 万」，一起加只是把每轮抓取成本翻倍。镜像源的特征就是 NEW 很大而 ADDS≈0，判为 `REDUNDANT`。

基线只包含**默认启用**的源抓到的地址，所以镜像了某个「已配置但默认关闭」的源的候选，ADDS 会偏高。要把关掉的源也算进来：`go run ./cmd/fetchproxies -all -out baseline.txt`。

产出量远超 `MaxRawProxies`（4000）的源要默认关闭：一轮就能填满原始池，而 `Trim` 按分数淘汰、新代理都是 `ScoreInit`，等于按平局随机踢掉其它源的代理。`solispirit-http`（123010 条，30 倍于上限）和 `casals-*` 因此默认关闭。

### 扫代理入池

三条入池路径，按量和权限选：

| 方式 | 机制 | 适合 |
|------|------|------|
| `scan-to-pool.sh` | 直接调 store 的 `AddRaw` / `MarkValidated`，批量写 | 几千～几万条，本机有 Redis 权限 |
| `submit-proxies.sh` | 走 `POST /api/proxies/submit`，分批推 | 任意量，**不需要 Redis 权限，可远程** |
| `import-proxies.sh` | 走 `POST /api/proxies/test`，一条一个请求 | 几百条以内，面板在跑 |

```bash
./scripts/scan-to-pool.sh                       # 试跑：扫 + 验活，只报告
./scripts/scan-to-pool.sh --write               # 落库
./scripts/scan-to-pool.sh --in live.txt --write  # 从文件导入
./scripts/scan-to-pool.sh --family ipv6 --write
./scripts/scan-to-pool.sh --redis-db 15 --write   # 先拿空库试手
make scan-to-pool WRITE=1

# 走 HTTP，不碰 Redis，能从别的机器跑
./scripts/submit-proxies.sh -f proxies.txt --public          # 内网免鉴权
./scripts/submit-proxies.sh -f proxies.txt --token upp_xxx   # 带 token
./scripts/submit-proxies.sh -f proxies.txt --token upp_xxx --test  # 提交后验活
cat proxies.txt | ./scripts/submit-proxies.sh --public       # 从管道
```

`submit-proxies.sh` 自动按 500 条分批提交（避开请求体上限）、按 200 条分批验活（服务端 `maxItems`），并且报的是**池子净增**而不只是新增条数 —— 原因见上面 submit 接口那节。

**默认是试跑**，只报告会发生什么。写的是共享数据，所以 `--write` 必须显式给。

抓取时每条代理保留原始来源名（`Source`），面板的按源质量统计才有意义；`--in` 从文件读没有来源信息，统一标 `scan`，可用 `--source` 覆盖。

**注意池子可能净减少。** 实测一次：写入 563 条新地址、89 条验活通过，池子只涨 52 条，`Trim` 淘汰了 511 条。原始池到 `MaxRawProxies`（4000）上限后按分数淘汰，而新抓的代理分数都是 `ScoreInit`，等于按平局随机踢 —— 踢掉的可能是其它源的代理。

这个损失在计数上看不出来：插入和淘汰在 raw 数字上互相抵消（`raw+0`），总数还是正的（`total+52`）。所以 `scanproxies` 用 `AddRaw` 的实际插入数反推淘汰量，单独报出来：

```
delta:  total+52 validated+52 raw+0

note: Trim evicted 511 proxies to stay under the raw cap (4000).
```

一次别扫太多（用 `--limit`）。验活之后测出是死的地址**不会**入库：它们以 `ScoreInit` 进原始池、占掉配额、逼 `Trim` 去淘汰别的源的代理，而我们刚刚才证明它们不通。所以会看到这行：

```
skipping 231 address(es) measured dead — inserting them would
consume raw-cap space and evict working proxies from other sources
```

`--skip-validate` 时没有判定结果可依据，全部写入，交给面板的校验轮打分。

---

### AI 爬取代理入池

给 AI / 脚本用的专用入池接口：接收模型产出的代理列表，解析后写入免费代理池，并打上 `ai-*` 来源标签。

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|------|------|
| POST | `/api/ai-proxy?source=ai-claude` | session 或 Bearer token | AI 代理批量入池 |

**Body 支持：**

1. JSON 对象：`{"proxies":["1.1.1.1:80",{"host":"2.2.2.2","port":1080,"protocol":"socks5"}]}`（也认 `hosts` / `items` / `list`，以及 `data` 下的嵌套列表）
2. JSON 数组：`["1.1.1.1:80",{"host":"2.2.2.2","port":443,"protocol":"socks5"}]`
3. 纯文本：每行一条 `host:port` 或 `proto://host:port`（`#` 开头为注释）

请求体上限 1 MB，超出返回 `413`；按标准化后的 `host:port` 自动去重。支持 `http` / `https` / `socks4` / `socks5`，并将 `socks` / `socks5h` 归一为 `socks5`。`source` 未带 `ai-` 前缀时会自动补上。

```bash
# 面板登录后 cookie 即可；脚本用 token
curl -sS -X POST 'http://127.0.0.1:7891/api/ai-proxy?source=ai-claude' \
  -H 'Authorization: Bearer upp_xxx' \
  -H 'Content-Type: application/json' \
  -d '{"proxies":["1.1.1.1:80","2.2.2.2:443"]}'

curl -sS -X POST 'http://127.0.0.1:7891/api/ai-proxy?source=gpt' \
  -H 'Authorization: Bearer upp_xxx' \
  -H 'Content-Type: text/plain' \
  --data-binary @proxies.txt
```

响应与 `/api/proxies/submit` 相同（`submitted` / `added` / `duplicates` / `net_growth` / `evicted` / `raw_at_cap`），并多返回 `source`、`rejected`；存在输入内重复时还会返回 `input_duplicates`。其中 `duplicates` 已包含输入内重复和池中已有记录。

面板路径：**节点 → AI 爬取**（`/ai-proxy`），可粘贴列表一键提交。

### AI 搜索与提示词管理

面板上的「AI 搜索」调用任意 OpenAI 兼容的 `/chat/completions` 接口（如 OpenAI、DeepSeek、本地 vLLM/Ollama 网关），按内置提示词把网页内容 / 关键词转成代理候选列表，可预览后一键并入入池区。提示词内置且可修改。

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|------|------|
| POST | `/api/ai-search` | session 或 Bearer token | 调 AI 搜索，返回解析后的代理候选（不入池） |
| GET | `/api/ai-prompts` | session 或 Bearer token | 列出全部提示词模板（内置 + 已修改） |
| PUT | `/api/ai-prompts` | session 或 Bearer token | 新增 / 修改提示词模板 |
| DELETE | `/api/ai-prompts?name=proxy_extract` | session 或 Bearer token | 恢复内置默认（内置词）或删除（自定义词） |

`POST /api/ai-search` 请求体：

```json
{
  "url": "https://api.deepseek.com/chat/completions",
  "apikey": "sk-...",
  "model": "deepseek-chat",
  "level": 6,
  "prompt_key": "proxy_extract",
  "content": "粘贴的网页内容或搜索关键词"
}
```

- `level`：0–10 思考深度，影响采样温度与输出 token 上限（越大越深入）。
- `prompt_key`：使用的提示词模板；`prompt` 传非空时直接覆盖 system 提示词。
- `content` 为空时，模型按提示词自主生成候选（用于 `proxy_ai_generate` 等模板）。

响应：`{"success":true,"data":{"raw":"AI 原始返回","proxies":[{"host":"1.2.3.4","port":8080,"protocol":"http"}],"count":N}}`。`proxies` 可直接回传 `/api/ai-proxy` 入池。

`GET /api/ai-prompts` 返回模板数组，每项含 `name` / `title` / `description` / `system` / `user` / `builtin`；`builtin:true` 表示内置模板，`PUT` 修改后 `default` 变为 `false`。`user` 中的 `{{.Content}}` 会被替换为搜索内容，`{{.Count}}` 替换为候选数量（默认 50）。

```bash
# 保存自定义提示词
curl -sS -X PUT 'http://127.0.0.1:7891/api/ai-prompts' \
  -H 'Authorization: Bearer upp_xxx' -H 'Content-Type: application/json' \
  -d '{"name":"proxy_extract","title":"提取免费代理列表","system":"你是代理提取器…","user":"内容：{{.Content}}"}'

# 恢复内置默认
curl -sS -X DELETE 'http://127.0.0.1:7891/api/ai-prompts?name=proxy_extract' \
  -H 'Authorization: Bearer upp_xxx'
```

面板路径：**节点 → AI 爬取**（`/ai-proxy`），「AI 搜索」区块可填接口 URL / API Key / 模型 / 思考等级，点铅笔图标编辑当前提示词。


## 自动打野

一条闭环：抓 → 验 → 测 → **记** → **调**。前三步补池子，后两步让池子的输入自己变好。

| 命令 | 作用 |
|------|------|
| `./scripts/auto-enrich.sh` | 一轮：抓取 → 验活 → 只写活的 |
| `go run ./cmd/sourceyield` | 抽样实测每个源的**活代理**产出，给出开关建议 |
| `go run ./cmd/sourceyield-report` | 查看历史测量记录与趋势 |
| `go run ./cmd/sourcetune` | **按历史**决定关掉/开回哪些源 |
| `./scripts/discover-sources.sh` | 找新源（详见上面「发现新源」） |

```bash
make source-yield                       # 各源产出实测 + 建议的开关命令
make source-yield PERSIST=1             # 落库，用于趋势分析
make source-report                      # 看历史与趋势
make source-tune                        # 试跑：打印开关计划
make source-tune APPLY=1                # 真按历史调开关
make auto-enrich                        # 试跑一轮
make auto-enrich WRITE=1                # 落库
make auto-enrich WRITE=1 PERSIST=1      # 落库 + 记录源质量
make auto-enrich WRITE=1 PERSIST=1 TUNE=1   # 全套闭环
./scripts/auto-enrich.sh --write --yield --persist --tune-apply --discover
go run ./cmd/sourceyield-report -name rdavydov-socks4
go run ./cmd/sourceyield-report -trend-only      # 只看有趋势数据的
```

**测量和决策是分开的两步**，因为一次测量不足以下结论：`sourceyield -persist` 只记录，`sourcetune` 读几轮历史才动手。

### 为什么要测「活代理产出」

面板的 `last_ok` 数的是**入库条数**，不是**能用的条数**，据此判断源的好坏会得出反的结论。实测对比：

| 源 | 抓到 | 抽样存活 | 折算每轮活代理 | 建议 |
|----|------|----------|----------------|------|
| `rdavydov-socks4` | 630 | 16/60 | ~168 | KEEP |
| `b4rc0de-socks5` | 257 | 11/60 | ~47 | KEEP |
| `prxchk-http` | 58 | 0/58 | 0 | DISABLE |

排序看 `EST/RND`（抽样存活率 × 全量产出），不是 `FETCHED`。一个发几万条全死地址的源，比发 100 条其中 40 条能用的源更糟：两者都要每轮抓一次，前者还会挤掉别人的代理。

小样本不给结论。抽样数低于 `-min-sample`（默认 20）报 `UNKNOWN` 而不是编一个判定 —— 否则一次网络抖动就能让某个源被永久关掉。存活率带 Wilson 置信区间：0/58 报的是「真实存活率低于 6.2%」，而不是「存活率 0%」。

`-emit-toggles` 只**打印**关源命令，不执行。关源改的是共享状态，而且 `toggle` 是翻转不是设置 —— 同一条跑两次会把源又打开。

### 持久化与趋势分析

`sourceyield -persist` 把每次测量存进 Redis（每源一个 ZSET，保留 90 天或 500 条），`sourceyield-report` 可以：

- 看某个源最近 N 次测量，判断是在改善（IMPROVING）、衰退（DEGRADING）还是稳定（STABLE）
- 对比不同时间点的产出，避免因一次偶然波动就错杀好源

趋势判断：把最近 N 条记录切成前后两半，比较平均存活率。前半比后半高 >5% → IMPROVING，低 >5% → DEGRADING，否则 STABLE。不足 2 条记录报 INSUFFICIENT —— 一条记录说明不了方向，报「稳定」等于鼓励凭一晚的网络状况关源。

存的是每源一个 ZSET（`upp:sourceyield:h:<源名>`，score 是测量时间），加一个源名索引集合。不用「一个测量一个键 + `KEYS` 扫」：`KEYS` 会遍历整个 keyspace 并阻塞 Redis 的单线程事件循环，本项目的 Lua 版 `AvgScore` 就是因为这个被回退的。

`-persist` 拒绝空地址。go-redis 会把空 `Addr` 默默替换成 `localhost:6379` db 0，也就是生产池 —— 这里宁可报错退出（退出码 2）。

### 按历史调开关

`sourcetune` 读 `-persist` 攒下的历史，输出每个源该怎么办。默认**只打印**，`-apply` 才真改：

```
SOURCE                     STATE    ACTION     TREND        REASON
prxchk-http                on       disable    STABLE       3 consecutive measurements found nothing alive (newest 0/60)
mmpx12-http                off      enable     STABLE       recovered: 14/60 alive, ~140 working per round
a2u-free-proxy-list        on       keep       DEGRADING    1 dead measurement(s) in a row, needs 3 to disable
clarketm-proxy-list        on       skip       INSUFFICIENT only 1 measurement(s), need 3 before judging
```

| 参数 | 默认 | 作用 |
|------|------|------|
| `-min-runs` | 3 | 不够这么多次测量就 `skip`，不下结论 |
| `-dead-streak` | 3 | 连续这么多轮读作死才关 |
| `-abort-ratio` | 0.5 | 多数源同时读作死就整体拒绝执行 |
| `-min-enabled` | 3 | 保底开着的源数，不会把输入关光 |
| `-min-estimate` | 1 | 开回一个源至少要有这么多活代理产出 |

四条不对称的安全线，都是因为**关源自动、开源手工**：

1. **要连续几轮都死才关**（`-dead-streak`）。一次网络抖动不该让源被永久关掉。
2. **多数源同时读作死 → 整体拒绝，退出码 3**。源不会一起坏；这个形状说明校验 URL 被墙或本机断网。照着关会把整个池子的输入关光。测量太少的源不计入这个比例，否则几个新源就能把判断带偏。
3. **保底源数**（`-min-enabled`）。被这条挡下的关源操作会在 REASON 里写明是保底线拦的，不会看起来像规则没生效。
4. **`OVERSIZED` 的源不会被开回来**。它的问题是量太大（一轮就填满 `MaxRawProxies`），不是不活；开回来只会让 `Trim` 去淘汰别的源的代理。

用的是 `SetScraperEnabled`（**设置**，不是面板 `/toggle` 的**翻转**），所以重复跑不会把源又关回去 —— 第二次跑报 `Nothing to change`。每次改动都会写进面板的事件流，池子形状变了能查到是谁改的。

### 挂 cron

```cron
*/30 * * * * cd /path/to/unified-proxy-pool && ./scripts/auto-enrich.sh --write >> logs/auto-enrich.log 2>&1
0 4 * * *    cd /path/to/unified-proxy-pool && ./scripts/auto-enrich.sh --write --yield --persist --tune-apply --discover >> logs/auto-enrich.log 2>&1
```

每天 4 点那一轮：测各源产出 → 记进历史 → 按历史调开关。头几天只会看到 `skip`（`-min-runs` 要 3 次测量），攒够之后才开始动手，这是有意的。

想先看不改：把 `--tune-apply` 换成 `--tune`，只打印计划。`--tune-apply` 也要配 `--write` 才真改开关。

脚本里 `sourcetune` 是先编译成二进制再跑，不用 `go run`：`go run` 把子进程退出码一律压成 1，「拒绝执行（3）」和「自己崩了（1）」就分不开了，脚本也没法分别处理。

带 `flock` 非阻塞锁：一轮抓取加验活可能十几分钟，cron 每 30 分钟拉一次会重叠，两个进程同时写 Redis 会互相触发 `Trim`。重叠时直接跳过并以 0 退出，cron 不会误报失败。用非阻塞而非排队，是因为排队会让积压任务在网络恢复后一起涌出去。

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
