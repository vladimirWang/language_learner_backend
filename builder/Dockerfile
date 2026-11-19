FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    UV_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple \
    UV_CACHE_DIR=/builder/.uv-cache \
    UV_LINK_MODE=copy \
    HF_ENDPOINT=https://hf-mirror.com \
    WHISPER_MODEL=small

# ffmpeg：音频转码与按题切片；tesseract：视频帧 OCR 提取选项文字
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates ffmpeg tesseract-ocr tesseract-ocr-deu tesseract-ocr-eng && \
    rm -rf /var/lib/apt/lists/*

# 安装 uv（用 pip + 清华源，绕开 ghcr.io 被墙的问题）
RUN pip install --no-cache-dir -i https://pypi.tuna.tsinghua.edu.cn/simple uv

WORKDIR /builder
COPY builder/pyproject.toml builder/uv.lock ./
RUN uv sync --locked --no-dev && \
    rm -rf $UV_CACHE_DIR

COPY builder/builder ./builder
COPY content/ ./content/

ENV CONTENT_DIR=/builder/content \
    WORK_DIR=/builder/work

ENTRYPOINT ["uv", "run", "python", "-m", "builder"]
