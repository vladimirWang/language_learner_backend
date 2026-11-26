#!/usr/bin/env bash
# ACR 一键部署：本机/服务器登录 → 构建并推送镜像 → 服务器拉取并重启 → 健康检查。
#
# 默认使用单仓库多 tag：registry.cn-hangzhou.aliyuncs.com/fishwang/mycentos。
# 杭州 ACR + 上海 ECS 必须使用公网地址，不要用 registry-vpc.*。
#
# 用法：
#   ./deploy_acr.sh
#   SKIP_ACR_LOGIN=1 ./deploy_acr.sh   # 本机和服务器都已登录时跳过登录
#
# 可覆盖：ACR_REGISTRY / ACR_NAMESPACE / ACR_REPOSITORY / ACR_USERNAME /
#         SERVER / REMOTE_ROOT / PULL_TIMEOUT

set -euo pipefail

cd "$(dirname "$0")"

ACR_REGISTRY="${ACR_REGISTRY:-registry.cn-hangzhou.aliyuncs.com}"
ACR_NAMESPACE="${ACR_NAMESPACE:-fishwang}"
ACR_REPOSITORY="${ACR_REPOSITORY:-mycentos}"
ACR_USERNAME="${ACR_USERNAME:-ruijin45}"
SERVER="${SERVER:-root@139.224.68.145}"
REMOTE_ROOT="${REMOTE_ROOT:-/root/lang_app}"
STAMP="${STAMP:-$(date +%Y%m%d-%H%M)}"

ACR_REPO="$ACR_REGISTRY/$ACR_NAMESPACE/$ACR_REPOSITORY"
IMG_PY_BASE="$ACR_REPO:python-base-py312"
IMG_PARSER="$ACR_REPO:listen-parser-prod"
IMG_SERVICE="$ACR_REPO:service-prod"
IMG_SERVICE_VERSIONED="$ACR_REPO:service-prod-$STAMP"
IMG_NGINX="$ACR_REPO:nginx-prod"
IMG_PG="$ACR_REPO:pg-pg17"
IMG_MIGRATE="$ACR_REPO:migrate-v4.18.3"

for cmd in docker make ssh; do
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "❌ 缺少命令：$cmd" >&2
    exit 1
  }
done

docker buildx version >/dev/null 2>&1 || {
  echo "❌ Docker buildx 不可用" >&2
  exit 1
}

if [[ "${SKIP_ACR_LOGIN:-0}" != "1" ]]; then
  password="${ACR_PASSWORD:-}"
  if [[ -z "$password" ]]; then
    read -r -s -p "请输入 ACR 访问凭证密码（账号：$ACR_USERNAME）: " password
    echo
  fi
  if [[ -z "$password" ]]; then
    echo "❌ ACR 密码不能为空" >&2
    exit 1
  fi

  echo "==> 1/4 登录本机与服务器 ACR ..."
  printf '%s' "$password" | docker login \
    --username "$ACR_USERNAME" --password-stdin "$ACR_REGISTRY"
  printf '%s' "$password" | ssh -o ConnectTimeout=8 "$SERVER" \
    docker login --username "$ACR_USERNAME" --password-stdin "$ACR_REGISTRY"
  unset password ACR_PASSWORD
else
  echo "==> 1/4 跳过 ACR 登录（SKIP_ACR_LOGIN=1）"
fi

echo "==> 2/4 构建并推送 ACR 镜像 ..."
# 命令行变量优先级高于 Makefile 内的默认值；无需手动 docker tag。
make \
  IMG_PY_BASE="$IMG_PY_BASE" \
  IMG_PARSER="$IMG_PARSER" \
  IMG_SERVICE="$IMG_SERVICE" \
  IMG_SERVICE_VERSIONED="$IMG_SERVICE_VERSIONED" \
  IMG_NGINX="$IMG_NGINX" \
  IMG_PG="$IMG_PG" \
  IMG_MIGRATE="$IMG_MIGRATE" \
  push-all

echo "==> 3/4 部署到 $SERVER ..."
IMG_SERVICE="$IMG_SERVICE" \
IMG_NGINX="$IMG_NGINX" \
IMG_PG="$IMG_PG" \
IMG_MIGRATE="$IMG_MIGRATE" \
SERVER="$SERVER" \
REMOTE_ROOT="$REMOTE_ROOT" \
  ./deploy_at_server.sh

echo "==> 4/4 完成"
echo "✅ 当前版本：$IMG_SERVICE"
echo "✅ 回滚版本：$IMG_SERVICE_VERSIONED"
