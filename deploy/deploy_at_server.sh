#!/usr/bin/env bash
# 本地执行：make push-all 成功后，让服务器拉取新镜像并重启服务。
#
# 等价命令：
#   ./deploy_all.sh root@139.224.68.145 /root/lang_app
#
# 默认参数与 deploy_all.sh 保持一致，可用环境变量覆盖：
#   SERVER=root@1.2.3.4 REMOTE_ROOT=/opt/lang-app ./deploy_at_server.sh
#
# 前置：
#   1. 本地已执行 make push-all（镜像已推到 Docker Hub）
#   2. 服务器已配置 SSH 免密登录，并能 pull 镜像

set -euo pipefail

cd "$(dirname "$0")"

SERVER="${SERVER:-root@139.224.68.145}"
REMOTE_ROOT="${REMOTE_ROOT:-/root/lang_app}"

exec ./deploy_all.sh "$SERVER" "$REMOTE_ROOT" "$@"
