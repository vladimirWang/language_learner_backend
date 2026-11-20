#!/usr/bin/env bash
# 一键部署：上传代码 + 镜像包 → docker load → 启动服务 → 健康检查
#
# 用法：
#   ./deploy_all.sh <user@host> [remote_root]
#
# 示例：
#   ./deploy_all.sh root@1.2.3.4
#   ./deploy_all.sh ubuntu@mp.hetou.vip /opt/lang-app
#
# 前置：
#   1. 本地已构建并导出镜像包：make export-all
#   2. 服务器已配好 SSH 免密登录
#   3. 服务器已安装 Docker + compose v2
#
# 服务器目录结构（与本地保持一致，因为 compose 用相对路径挂载）：
#   <remote_root>/
#   └── language_learner_backend/{deploy,content,builder}
#
# 说明：前端产物（deploy/nginx/html）由前端独立上传，本脚本不参与，
#       同步时也会排除该目录，避免覆盖线上前端文件。

set -euo pipefail

cd "$(dirname "$0")"

SERVER="${1:-root@139.224.68.145}"
REMOTE_ROOT="${2:-/root/lang_app}"
TAR_PATH="dist-images/lang-images-prod-amd64.tar.gz"
TAR_NAME="$(basename "$TAR_PATH")"
LOCAL_ROOT="$(cd ../.. && pwd)"

if [[ -z "$SERVER" ]]; then
  echo "用法：$0 <user@host> [remote_root]" >&2
  echo "示例：$0 root@1.2.3.4 /opt/lang-app" >&2
  exit 1
fi

if [[ ! -f "$TAR_PATH" ]]; then
  echo "❌ 找不到镜像包：$TAR_PATH" >&2
  echo "   先运行 make export-all 生成" >&2
  exit 1
fi

echo "📦 镜像包：${TAR_PATH} ($(du -sh "$TAR_PATH" | cut -f1))"
echo "🎯 服务器：$SERVER"
echo "📁 远程根目录：$REMOTE_ROOT"
echo

echo "==> 1/6 检查 SSH 与 Docker ..."
ssh -o ConnectTimeout=5 -o BatchMode=yes "$SERVER" \
  'command -v docker >/dev/null && docker compose version >/dev/null && echo ok' \
  >/dev/null
echo "    ✅ SSH 可达，Docker + Compose 已安装"

echo "==> 2/6 创建远程目录 ..."
ssh "$SERVER" "mkdir -p '$REMOTE_ROOT/language_learner_backend'"

echo "==> 3/6 同步业务文件（deploy / content / builder）..."
COPYFILE_DISABLE=1 tar czf - -C "$LOCAL_ROOT" \
  --no-xattrs \
  --exclude='.DS_Store' \
  --exclude='._*' \
  --exclude='*/deploy/data' \
  --exclude='*/deploy/dist-images' \
  --exclude='*/deploy/nginx/html' \
  --exclude='*/builder/.venv' \
  --exclude='*/builder/work' \
  --exclude='*/builder/.uv-cache' \
  --exclude='*/__pycache__' \
  --exclude='*.pyc' \
  language_learner_backend/deploy \
  language_learner_backend/content \
  language_learner_backend/builder \
  | ssh "$SERVER" "tar xzf - -C '$REMOTE_ROOT'"
echo "    ✅ 业务文件已同步"

echo "==> 4/6 上传镜像包 ${TAR_NAME} ($(du -sh "$TAR_PATH" | cut -f1))..."
# 镜像没变化时跳过上传（例如只改了挂载进容器的 builder/content，无需重建镜像）。
LOCAL_SHA="$(shasum -a 256 "$TAR_PATH" | awk '{print $1}')"
REMOTE_SHA="$(ssh "$SERVER" "sha256sum '$REMOTE_ROOT/$TAR_NAME' 2>/dev/null | awk '{print \$1}'" || true)"
SKIP_UPLOAD=0
if [[ -n "$REMOTE_SHA" && "$LOCAL_SHA" == "$REMOTE_SHA" ]]; then
  echo "    ⏭  服务器已有相同镜像包（sha256=${LOCAL_SHA:0:12}），跳过上传与 load"
  SKIP_UPLOAD=1
else
  # rsync 支持断点续传，网络中断后重跑不会从头再传；没有 rsync 时回退 scp。
  if command -v rsync >/dev/null 2>&1; then
    rsync -h -I --partial --progress "$TAR_PATH" "$SERVER:$REMOTE_ROOT/$TAR_NAME"
  else
    scp "$TAR_PATH" "$SERVER:$REMOTE_ROOT/$TAR_NAME"
  fi
  echo "    ✅ 上传完成"
fi

echo "==> 5/6 docker load + compose up ..."
ssh "$SERVER" bash -s <<REMOTE
set -euo pipefail
cd "$REMOTE_ROOT/language_learner_backend/deploy"
if [[ "$SKIP_UPLOAD" != "1" ]]; then
  docker load < "$REMOTE_ROOT/$TAR_NAME"
fi
ln -sf .env.prod .env
mkdir -p data/builder-work data/builder-cache
docker compose -f docker-compose.prod.yml up -d
REMOTE
echo "    ✅ 服务已启动"

echo "==> 6/6 健康检查 ..."
for i in $(seq 1 20); do
  if ssh "$SERVER" 'curl -fsS -m 2 http://127.0.0.1:8080/api/health' >/dev/null 2>&1; then
    echo "    ✅ version-service 健康"
    break
  fi
  if [[ "$i" -eq 20 ]]; then
    echo "    ⚠️  健康检查超时，请查看日志："
    echo "       ssh $SERVER 'cd $REMOTE_ROOT/language_learner_backend/deploy && docker compose -f docker-compose.prod.yml logs --tail=50'"
    exit 1
  fi
  sleep 2
done

echo
echo "🎉 部署完成"
echo
echo "后续可选："
echo "  首次导入素材: ssh $SERVER 'cd $REMOTE_ROOT/language_learner_backend/deploy && docker compose -f docker-compose.prod.yml --profile build run --rm builder --lang de'"
echo "  释放上传包  : ssh $SERVER 'rm -f $REMOTE_ROOT/$TAR_NAME'"
