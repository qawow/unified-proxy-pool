.PHONY: frontend build run test health lan sources check-sources fetch-proxies discover-sources scan-to-pool submit-proxies source-yield source-report source-tune auto-enrich check-dockerfile docker

GOPROXY ?= https://goproxy.cn,direct
DATA_DIR ?= ./data
REDIS_ADDR ?= 127.0.0.1:6379

# Release flags, kept in sync with Dockerfile:
#   -s -w      strip symbol table + DWARF (~30% smaller; panic traces stay
#              readable because Go resolves them via its own pclntab)
#   -trimpath  drop absolute build paths for reproducible builds
GO_BUILD_FLAGS ?= -buildvcs=false -trimpath -ldflags="-s -w"

frontend:
	cd frontend && npm install && npm run build

build: frontend
	GOPROXY=$(GOPROXY) go build $(GO_BUILD_FLAGS) -o bin/unified-proxy-pool ./cmd/app

run:
	mkdir -p $(DATA_DIR)
	GOPROXY=$(GOPROXY) DATA_DIR=$(DATA_DIR) REDIS_ADDR=$(REDIS_ADDR) \
		DIRECT_PROXY_ADDR=0.0.0.0:7892 \
		FREE_VALIDATE_URL=https://www.gstatic.com/generate_204 \
		go run -buildvcs=false ./cmd/app

check-dockerfile:
	bash scripts/check-dockerfile.sh

test: check-dockerfile
	GOPROXY=$(GOPROXY) go test -buildvcs=false ./...

docker: check-dockerfile
	docker build -t unified-proxy-pool:local .

health:
	curl -s http://127.0.0.1:7891/api/health | python3 -m json.tool

lan:
	./examples/lan-client.sh

# 采集源相关：都不需要面板在跑，复用面板里的同一套 crawler 代码
sources:
	GOPROXY=$(GOPROXY) go run -buildvcs=false ./cmd/sources -enabled

# 源健康检查：ok / SILENT（能连但解析出 0 条）/ FAILED
check-sources:
	./scripts/check-sources.sh --stats

# 抓取 + 验活 → out/proxies-<时间>.txt
fetch-proxies:
	./scripts/fetch-proxies.sh --check

# 探测候选源，报告哪些值得加（看 ADDS 列，不是 NEW 列）
discover-sources:
	./scripts/discover-sources.sh --emit-go

# 扫代理写进池子。默认试跑；真写要 WRITE=1，因为改的是共享数据
scan-to-pool:
	./scripts/scan-to-pool.sh $(if $(filter 1,$(WRITE)),--write,)

# 走 HTTP API 入池，不需要 Redis 权限。FILE=... 必填，TEST=1 顺便验活
submit-proxies:
	@test -n "$(FILE)" || { echo "用法：make submit-proxies FILE=proxies.txt [TEST=1] [TOKEN=upp_xxx]"; exit 2; }
	./scripts/submit-proxies.sh -f "$(FILE)" \
		$(if $(TOKEN),--token $(TOKEN),--public) \
		$(if $(filter 1,$(TEST)),--test,)

# 抽样实测每个源的「活代理」产出，排序看 EST/RND 而不是原始条数
source-yield:
	GOPROXY=$(GOPROXY) go run -buildvcs=false ./cmd/sourceyield -emit-toggles $(if $(filter 1,$(PERSIST)),-persist,)

# 看历史测量与趋势（要先 source-yield PERSIST=1 攒几轮）
source-report:
	GOPROXY=$(GOPROXY) go run -buildvcs=false ./cmd/sourceyield-report

# 按历史产出调整源开关。默认试跑；APPLY=1 才真改，因为改的是共享状态
source-tune:
	GOPROXY=$(GOPROXY) go run -buildvcs=false ./cmd/sourcetune $(if $(filter 1,$(APPLY)),-apply,)

# 自动打野一轮：抓取 → 验活 → 只写活的。默认试跑，WRITE=1 才落库
auto-enrich:
	./scripts/auto-enrich.sh $(if $(filter 1,$(WRITE)),--write,) $(if $(filter 1,$(PERSIST)),--persist,) $(if $(filter 1,$(TUNE)),--tune-apply,)
