.PHONY: frontend build run test health lan

GOPROXY ?= https://goproxy.cn,direct
DATA_DIR ?= ./data
REDIS_ADDR ?= 127.0.0.1:6379

frontend:
	cd frontend && npm install && npm run build

build: frontend
	GOPROXY=$(GOPROXY) go build -buildvcs=false -o bin/unified-proxy-pool ./cmd/app

run:
	mkdir -p $(DATA_DIR)
	GOPROXY=$(GOPROXY) DATA_DIR=$(DATA_DIR) REDIS_ADDR=$(REDIS_ADDR) \
		DIRECT_PROXY_ADDR=0.0.0.0:7892 \
		FREE_VALIDATE_URL=https://www.gstatic.com/generate_204 \
		go run -buildvcs=false ./cmd/app

test:
	GOPROXY=$(GOPROXY) go test -buildvcs=false ./...

health:
	curl -s http://127.0.0.1:7891/api/health | python3 -m json.tool

lan:
	./examples/lan-client.sh
