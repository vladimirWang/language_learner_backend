#!/usr/bin/env bash
# 把镜像 tar.gz 上传到服务器并 docker load
#
# 用法：
#   ./push_and_load.sh <user@host> [remote_dir] [tar_path]
#
# 示例：
#   ./push_and_load.sh root@1.2.3.4
#   ./push_and_load.sh ubuntu@mp.hetou.vip /opt/lang-app
#   ./push_and_load.sh root@1.2.3.4 /opt/lang-app dist-images/lang-images-prod-amd64.tar.gz

set -euo pipefail

cd "$(dirname "$0")"

SERVER="${1:-}"
REMOTE_DIR="${2:-/opt/lang-app}"
TAR_PATH="${3:-dist-images/lang-images-prod-amd64.tar.gz}"

if [[ -z "$SERVER" ]]; then
  echo "用法：$0 <user@host> [remote_dir] [tar_path]" >&2
  echo "示例：$0 root@1.2.3.4 /opt/lang-app" >&2
  exit 1
fi

if [[ ! -f "$TAR_PATH" ]]; then
  echo "❌ 找不到镜像包：$TAR_PATH" >&2
  echo "   先运行 make export-all 生成" >&2
  exit 1
fi

TAR_NAME="$(basename "$TAR_PATH")"
SIZE="$(du -sh "$TAR_PATH" | cut -f1)"

echo "📦 镜像包：$TAR_PATH（$SIZE）"
echo "🎯 服务器：$SERVER"
echo "📁 远程目录：$REMOTE_DIR"
echo

echo "==> 1/4 测试 SSH 连接 ..."
ssh -o ConnectTimeout=5 -o BatchMode=yes "$SERVER" 'echo ok' >/dev/null

echo "==> 2/4 创建远程目录 ..."
ssh "$SERVER" "mkdir -p '$REMOTE_DIR'"

echo "==> 3/4 上传 $TAR_NAME ..."
scp "$TAR_PATH" "$SERVER:$REMOTE_DIR/$TAR_NAME"

echo "==> 4/4 服务器上 docker load ..."
ssh "$SERVER" "docker load < '$REMOTE_DIR/$TAR_NAME'"

echo
echo "✅ 镜像已加载到 $SERVER"
echo
echo "后续在服务器上执行："
echo "  cd $REMOTE_DIR"
echo "  ln -sf .env.prod .env"
echo "  docker compose up -d"
echo "  docker compose run --rm migrate"
echo "  docker compose run --rm builder --lang de"
