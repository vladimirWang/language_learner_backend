FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    UV_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple \
    UV_CACHE_DIR=/builder/.uv-cache \
    UV_LINK_MODE=copy

# 安装 uv（官方静态二进制）
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

WORKDIR /builder
COPY builder/pyproject.toml builder/uv.lock ./
RUN uv sync --locked --no-dev && \
    rm -rf $UV_CACHE_DIR

COPY builder/builder ./builder
COPY content/ ./content/

ENV CONTENT_DIR=/builder/content \
    WORK_DIR=/builder/work

ENTRYPOINT ["uv", "run", "python", "-m", "builder"]
