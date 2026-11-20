#!/usr/bin/env bash
# 构建 lang-service 镜像（linux/amd64）
# 该镜像已合并 Python builder 运行时，听力导入与 PDF 解析都在容器内执行
set -euo pipefail

# 无论从哪个目录调用，都切到脚本所在目录
cd "$(dirname "$0")"

./build_lang_service.sh

echo "✅ 镜像构建完成"
docker images --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}" | grep -E "lang-service|REPOSITORY"
