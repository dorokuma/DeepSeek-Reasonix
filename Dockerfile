# ── Dockerfile — Reasonix 多阶段构建 ──────────────────────────
# 构建最小化生产镜像:
#   docker build -t reasonix:latest .
#
# 配合 deploy.yaml 运行:
#   docker compose -f deploy.yaml up -d
#
# 或直接运行 CLI:
#   docker run --rm -it -v $(pwd)/reasonix.toml:/app/reasonix.toml reasonix:latest reasonix chat

# ── Stage 1: 构建阶段 ─────────────────────────────────────────
ARG GO_VERSION=1.25
FROM golang:${GO_VERSION}-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# 缓存依赖下载
COPY go.mod go.sum ./
RUN go mod download

# 构建应用
COPY . .
ARG APP_VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w -X main.version=${APP_VERSION}" \
    -trimpath \
    -o /reasonix ./cmd/reasonix

# ── Stage 2: 开发/调试镜像 ────────────────────────────────────
FROM builder AS dev

# 安装常用调试工具
RUN apk add --no-cache \
    bash \
    curl \
    jq \
    vim \
    busybox-extras

COPY --from=builder /reasonix /usr/local/bin/reasonix

WORKDIR /app
CMD ["reasonix"]

# ── Stage 3: 最小化生产镜像 ───────────────────────────────────
FROM scratch AS production

# 复制 CA 证书（用于 HTTPS/TLS 请求）
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# 复制时区数据
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# 复制二进制
COPY --from=builder /reasonix /reasonix

# 默认路径
WORKDIR /app

# 健康检查
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
  CMD ["/reasonix", "doctor"]

# 默认入口
ENTRYPOINT ["/reasonix"]
CMD ["--help"]
