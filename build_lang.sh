#!/usr/bin/env bash
# 依次构建 lang-service 与 lang-builder 镜像（linux/amd64）
set -euo pipefail

# 无论从哪个目录调用，都切到脚本所在目录
cd "$(dirname "$0")"

./build_lang_service.sh
./build_lang_builder.sh

echo "✅ 全部镜像构建完成"
docker images --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}" | grep -E "lang-(service|builder)|REPOSITORY"
