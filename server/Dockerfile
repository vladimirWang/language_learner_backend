# 生产运行镜像：Go 版本服务 + Python builder 同容器。
#
# 系统依赖与 Python 依赖已固化在 lang-python-base 基础镜像中（见 Dockerfile.base），
# 听力解析二进制由 lang-listen-parser 镜像提供（见 Dockerfile.listen-parser），
# 本文件只负责 Go 编译与代码拷贝，通常 1 分钟内完成。
#
# 首次或不常改动时可直接用构建参数覆盖：
#   --build-arg BASE_IMAGE=... --build-arg PARSER_IMAGE=...
# 注意：全局 ARG 必须在第一个 FROM 之前声明，才能在 FROM 中展开。
ARG BASE_IMAGE=docker.io/furchtlos/lang-python-base:py312
ARG PARSER_IMAGE=docker.io/furchtlos/lang-listen-parser:prod

# ---------- 构建 Go 二进制 ----------
FROM golang:1.27-alpine AS go-builder
WORKDIR /src
# 国内加速：GOPROXY 走 goproxy.cn，避免 proxy.golang.org 被墙 EOF
ENV GOPROXY=https://goproxy.cn,direct
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/version-service ./cmd/version-service

# ---------- 听力解析二进制（取自单独推送的镜像） ----------
# COPY --from 不支持变量展开，因此用全局 ARG 定义一个中转阶段。
FROM ${PARSER_IMAGE} AS parser-artifact

# ---------- 运行阶段 ----------
FROM ${BASE_IMAGE}

ARG PARSER_IMAGE

WORKDIR /builder
COPY builder/builder ./builder
COPY content ./content
COPY --from=go-builder /out/version-service /app/version-service
# 独立听力解析二进制：设置 LISTEN_BIN=/usr/local/bin/listen-parser 即走二进制，
# 不设置仍保留 Python 通道（uv run --no-sync python -m builder.listen）。
COPY --from=parser-artifact /out/listen-parser /usr/local/bin/listen-parser

# builder 子进程的运行约定：工作目录 /builder，产物写到挂载的 /builder/work
ENV CONTENT_DIR=/builder/content \
    WORK_DIR=/builder/work \
    BUILDER_CMD=uv \
    BUILDER_ARGS="run --no-sync python -m builder" \
    BUILDER_DIR=/builder

EXPOSE 8080
ENTRYPOINT ["/app/version-service"]
