# 生产运行镜像：Go 版本服务 + Python builder 同容器。
#
# 听力导入 / PDF 解析等任务由 version-service 以子进程方式拉起
# （uv run --no-sync python -m builder.listen ...），因此运行阶段必须具备
# Python + uv + builder 依赖（ffmpeg / tesseract / faster-whisper 等）。

# ---------- 构建 Go 二进制 ----------
FROM golang:1.27-alpine AS go-builder
WORKDIR /src
# 国内加速：GOPROXY 走 goproxy.cn，避免 proxy.golang.org 被墙 EOF
ENV GOPROXY=https://goproxy.cn,direct
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/version-service ./cmd/version-service

# ---------- 运行阶段 ----------
FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    UV_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple \
    UV_LINK_MODE=copy \
    HF_ENDPOINT=https://hf-mirror.com \
    WHISPER_MODEL=small

# git / openssh-client 用于素材仓库 ls-remote；ffmpeg 用于听力切分；
# tesseract 用于视频帧 OCR
# 换阿里云源 + 重试，避免 deb.debian.org 在构建机网络上偶发 502
RUN sed -i 's|http://deb.debian.org|http://mirrors.aliyun.com|g; s|http://security.debian.org|http://mirrors.aliyun.com|g' \
        /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
    apt-get update && \
    apt-get install -y --no-install-recommends -o Acquire::Retries=3 \
        ca-certificates tzdata git openssh-client \
        ffmpeg tesseract-ocr tesseract-ocr-deu tesseract-ocr-eng && \
    rm -rf /var/lib/apt/lists/*

# 安装 uv（用 pip + 清华源，绕开 ghcr.io 被墙的问题）
RUN pip install --no-cache-dir -i https://pypi.tuna.tsinghua.edu.cn/simple uv

# 先装依赖再拷代码，代码变更时可命中缓存
WORKDIR /builder
COPY builder/pyproject.toml builder/uv.lock ./
RUN uv sync --locked --no-dev && rm -rf /root/.cache/uv

COPY builder/builder ./builder
COPY content ./content
COPY --from=go-builder /out/version-service /app/version-service

# builder 子进程的运行约定：工作目录 /builder，产物写到挂载的 /builder/work
ENV CONTENT_DIR=/builder/content \
    WORK_DIR=/builder/work \
    BUILDER_CMD=uv \
    BUILDER_ARGS="run --no-sync python -m builder" \
    BUILDER_DIR=/builder

EXPOSE 8080
ENTRYPOINT ["/app/version-service"]
