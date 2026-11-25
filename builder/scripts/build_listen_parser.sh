#!/usr/bin/env bash
# 构建独立听力解析二进制（产物：builder/dist/listen-parser）。
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PY=${PYTHON:-"$ROOT/.venv/bin/python"}

if [ ! -x "$PY" ]; then
  echo "未找到 Python 环境：$PY" >&2
  echo "请先在 builder 目录执行 uv sync" >&2
  exit 1
fi

if ! "$PY" -c "import PyInstaller" >/dev/null 2>&1; then
  echo "安装 PyInstaller..."
  if command -v uv >/dev/null 2>&1; then
    uv pip install --python "$PY" "pyinstaller>=6.11"
  else
    "$PY" -m pip install "pyinstaller>=6.11"
  fi
fi

"$PY" -m PyInstaller \
  --noconfirm \
  --clean \
  --distpath "$ROOT/dist" \
  --workpath "$ROOT/build" \
  "$ROOT/packaging/listen_parser.spec"

echo "已生成：$ROOT/dist/listen-parser"
