#!/usr/bin/env bash
# 一键部署：同步代码 → 服务器拉取镜像 → 启动服务 → 健康检查
#
# 用法：
#   ./deploy_all.sh <user@host> [remote_root]
#
# 示例：
#   ./deploy_all.sh root@1.2.3.4
#   ./deploy_all.sh ubuntu@mp.hetou.vip /opt/lang-app
#
# 前置：
#   1. 本地已执行 make push-all 推送镜像（首次需 docker login）
#   2. 服务器已配好 SSH 免密登录
#   3. 服务器已安装 Docker + compose v2，且能访问镜像仓库
#      私有仓库还需要在服务器执行一次 docker login
#
# 回滚到某个时间戳镜像：
#   IMG_SERVICE=docker.io/furchtlos/lang-service:prod-20261120-1530 ./deploy_all.sh root@host
#
# 离线备用（旧流程，上传分包 tar.gz；base 只传一次）：
#   make export-all && DEPLOY_MODE=tar ./deploy_all.sh root@host
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
DEPLOY_MODE="${DEPLOY_MODE:-registry}"
LOCAL_ROOT="$(cd ../.. && pwd)"

# 镜像仓库配置（与 Makefile 默认值保持一致，可用环境变量覆盖）
REGISTRY="${REGISTRY:-docker.io}"
NAMESPACE="${NAMESPACE:-furchtlos}"
IMG_SERVICE="${IMG_SERVICE:-$REGISTRY/$NAMESPACE/lang-service:prod}"
IMG_NGINX="${IMG_NGINX:-$REGISTRY/$NAMESPACE/lang-nginx:prod}"
IMG_PG="${IMG_PG:-$REGISTRY/$NAMESPACE/lang-pg:pg17}"
IMG_MIGRATE="${IMG_MIGRATE:-$REGISTRY/$NAMESPACE/lang-migrate:v4.18.3}"

# tar 模式按镜像拆包：service 每次重建，base（nginx/pg/migrate）通常只上传一次
SERVICE_TAR="dist-images/lang-service-prod-amd64.tar.gz"
BASE_TAR="dist-images/lang-base-prod-amd64.tar.gz"
LEGACY_TAR="dist-images/lang-images-prod-amd64.tar.gz"

if [[ -z "$SERVER" ]]; then
  echo "用法：$0 <user@host> [remote_root]" >&2
  echo "示例：$0 root@1.2.3.4 /opt/lang-app" >&2
  exit 1
fi

if [[ "$DEPLOY_MODE" == "tar" ]]; then
  if [[ ! -f "$SERVICE_TAR" && ! -f "$BASE_TAR" && ! -f "$LEGACY_TAR" ]]; then
    echo "❌ 找不到镜像包（${SERVICE_TAR} / ${BASE_TAR}）" >&2
    echo "   先运行 make export-all 生成" >&2
    exit 1
  fi
fi

echo "🚀 部署模式：${DEPLOY_MODE}"
if [[ "$DEPLOY_MODE" == "registry" ]]; then
  echo "📦 镜像：${IMG_SERVICE}"
  echo "        ${IMG_NGINX}"
  echo "        ${IMG_PG}"
  echo "        ${IMG_MIGRATE}"
else
  for f in "$BASE_TAR" "$SERVICE_TAR" "$LEGACY_TAR"; do
    [[ -f "$f" ]] && echo "📦 镜像包：$f ($(du -sh "$f" | cut -f1))"
  done
fi
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

COMPOSE_ENV="IMG_SERVICE='$IMG_SERVICE' IMG_NGINX='$IMG_NGINX' IMG_PG='$IMG_PG' IMG_MIGRATE='$IMG_MIGRATE'"

if [[ "$DEPLOY_MODE" == "registry" ]]; then
  echo "==> 4/6 服务器拉取镜像 ..."
  ssh "$SERVER" "cd '$REMOTE_ROOT/language_learner_backend/deploy' && \
    ln -sf .env.prod .env && \
    mkdir -p data/builder-work data/builder-cache"

  # 逐个镜像拉取：只显示 [i/N] + 镜像名，docker pull -q 屏蔽重复的 layer 进度。
  IMAGES=("$IMG_SERVICE" "$IMG_NGINX" "$IMG_PG" "$IMG_MIGRATE")
  total=${#IMAGES[@]}
  idx=0
  for img in "${IMAGES[@]}"; do
    idx=$((idx + 1))
    echo "    [$idx/$total] pulling $img"
    started=$(date +%s)
    job="$(date +%s)-$idx"
    log="/tmp/lang-pull-$job.log"
    done_file="/tmp/lang-pull-$job.done"
    # 在服务器 nohup 后台拉取：本地 SSH 断开不会中断下载，本地只负责轮询进度。
    ssh -o ServerAliveInterval=15 -o ServerAliveCountMax=3 "$SERVER" \
      "nohup sh -c 'timeout 600 docker pull -q $img > $log 2>&1; echo \$? > $done_file' >/dev/null 2>&1 &" \
      >/dev/null 2>&1
    rc=""
    while [[ -z "$rc" ]]; do
      sleep 10
      rc="$(ssh -o ConnectTimeout=5 "$SERVER" "cat $done_file 2>/dev/null" || true)"
      if [[ -z "$rc" ]]; then
        echo "            拉取中... $(( $(date +%s) - started ))s"
      fi
    done
    if [[ "$rc" != "0" ]]; then
      echo "    ❌ 拉取失败或超时（600s）：$img"
      echo "       服务器日志：$log"
      ssh "$SERVER" "tail -n 20 $log" 2>/dev/null || true
      exit 1
    fi
    ssh "$SERVER" "rm -f $log $done_file" >/dev/null 2>&1 || true
    echo "            done in $(( $(date +%s) - started ))s"
  done
  echo "    ✅ 镜像拉取完成（${total}/${total}）"

  echo "==> 5/6 compose up ..."
  ssh "$SERVER" "cd '$REMOTE_ROOT/language_learner_backend/deploy' && \
    $COMPOSE_ENV docker compose -f docker-compose.prod.yml up -d --quiet-pull"
  echo "    ✅ 服务已启动"
else
  # 按镜像分包：base 基本只传一次，service 每次重建；未变化的包跳过上传和 load。
  UPLOADED=()
  upload_pkg() {
    local file="$1" name local_sha remote_sha
    [[ -f "$file" ]] || return 0
    name="$(basename "$file")"
    local_sha="$(shasum -a 256 "$file" | awk '{print $1}')"
    remote_sha="$(ssh "$SERVER" "sha256sum '$REMOTE_ROOT/$name' 2>/dev/null | awk '{print \$1}'" || true)"
    if [[ -n "$remote_sha" && "$local_sha" == "$remote_sha" ]]; then
      echo "    ⏭  $name 未变化，跳过上传与 load"
      return 0
    fi
    echo "    ⬆️  $name ($(du -sh "$file" | cut -f1)) ..."
    if command -v rsync >/dev/null 2>&1; then
      rsync -h -I --partial --progress "$file" "$SERVER:$REMOTE_ROOT/$name"
    else
      scp "$file" "$SERVER:$REMOTE_ROOT/$name"
    fi
    UPLOADED+=("$name")
  }

  echo "==> 4/6 上传镜像包 ..."
  upload_pkg "$BASE_TAR"
  upload_pkg "$SERVICE_TAR"
  upload_pkg "$LEGACY_TAR"

  echo "==> 5/6 docker load（仅加载本次上传的包，-q 静默 layer 进度）..."
  ssh "$SERVER" bash -s -- "$REMOTE_ROOT" "${UPLOADED[@]+"${UPLOADED[@]}"}" <<'REMOTE'
set -euo pipefail
root="$1"; shift
cd "$root/language_learner_backend/deploy"
for f in "$@"; do
  echo "    load $f"
  docker load -q < "$root/$f"
done
ln -sf .env.prod .env
mkdir -p data/builder-work data/builder-cache
docker compose -f docker-compose.prod.yml up -d --quiet-pull
REMOTE
  echo "    ✅ 服务已启动"
fi

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
if [[ "$DEPLOY_MODE" == "tar" ]]; then
  echo "  释放上传包  : ssh $SERVER 'rm -f $REMOTE_ROOT/lang-{service,base}-prod-amd64.tar.gz'"
fi
