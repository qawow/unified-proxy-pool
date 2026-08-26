# syntax=docker/dockerfile:1

ARG BUILDPLATFORM
ARG TARGETPLATFORM

FROM --platform=$BUILDPLATFORM node:22-bookworm AS frontend
ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /src
COPY frontend/package.json frontend/package-lock.json ./frontend/
# lockfile may pin npmmirror; rewrite so GitHub-hosted builders can fetch.
RUN set -eux; \
    cd frontend; \
    sed -i "s#https://registry.npmmirror.com#${NPM_REGISTRY}#g" package-lock.json; \
    npm ci --registry="${NPM_REGISTRY}"
COPY frontend/ ./frontend/
RUN cd frontend && npm run build

FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG TARGETVARIANT=""
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=frontend /src/web/dist ./web/dist
RUN set -eux; \
    export CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}"; \
    if [ "${TARGETARCH}" = "arm" ] && [ -n "${TARGETVARIANT}" ]; then export GOARM="${TARGETVARIANT#v}"; fi; \
    go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/unified-proxy-pool ./cmd/app

FROM debian:bookworm-slim
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG TARGETVARIANT=""
ARG MIHOMO_VERSION=v1.19.22
# Empty default is required: `ARG FOO` with no `=` is unset, and `set -u` then
# aborts with "FOO: parameter not set" (GitHub Actions docker job, 2026-08-26).
ARG MIHOMO_ASSET=""

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl gzip \
    && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    os="${TARGETOS:-linux}"; \
    arch="${TARGETARCH:-amd64}"; \
    variant="${TARGETVARIANT:-}"; \
    version="${MIHOMO_VERSION:-v1.19.22}"; \
    asset="${MIHOMO_ASSET:-}"; \
    if [ "${os}" != "linux" ]; then \
        echo "unsupported TARGETOS: ${os}" >&2; \
        exit 1; \
    fi; \
    if [ -z "${asset}" ]; then \
        case "${arch}" in \
            amd64|386|arm64|ppc64le|riscv64|s390x) mihomo_arch="${arch}" ;; \
            arm) \
                case "${variant#v}" in \
                    5|6|7) mihomo_arch="armv${variant#v}" ;; \
                    "") mihomo_arch="armv7" ;; \
                    *) \
                        echo "unsupported TARGETVARIANT for arm: ${variant}" >&2; \
                        exit 1; \
                        ;; \
                esac \
                ;; \
            *) \
                echo "unsupported TARGETARCH: ${arch}" >&2; \
                exit 1; \
                ;; \
        esac; \
        asset="mihomo-${os}-${mihomo_arch}-${version}.gz"; \
    fi; \
    curl --retry 5 --retry-all-errors --retry-delay 2 -fsSL \
        "https://github.com/MetaCubeX/mihomo/releases/download/${version}/${asset}" \
        -o /tmp/mihomo.gz; \
    gunzip /tmp/mihomo.gz; \
    mv /tmp/mihomo /usr/local/bin/mihomo; \
    chmod +x /usr/local/bin/mihomo; \
    mihomo -v >/dev/null

WORKDIR /app
COPY --from=builder /out/unified-proxy-pool /usr/local/bin/unified-proxy-pool

VOLUME ["/data"]
EXPOSE 7891 7892 7893
ENV DATA_DIR=/data
ENV MIHOMO_BINARY=/usr/local/bin/mihomo
ENV PANEL_HOST=0.0.0.0
ENV PANEL_PORT=7891
ENV DIRECT_PROXY_ENABLED=true
ENV DIRECT_PROXY_ADDR=0.0.0.0:7892
ENV PROXY_CHAIN_ENABLED=true
ENV PROXY_CHAIN_ADDR=0.0.0.0:7893
ENV PROXY_CHAIN_HOPS=2
ENV FREE_PROXY_ENABLED=true
ENV REDIS_ADDR=127.0.0.1:6379

HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=5 \
  CMD curl -fsS http://127.0.0.1:7891/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/unified-proxy-pool"]
