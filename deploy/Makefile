# deploy/Makefile —— Mac 本地构建 + 打包 + 服务器部署
#
# 前提：Mac 上 Docker Desktop 已启用 buildx（默认就有）
# 服务器假设是 amd64（绝大多数云服务器），如果是 arm64 改 PLATFORM
#
# 使用：
#   1. make build-all        # 构建全部 4 个镜像（Mac 本地）
#   2. make export-all       # 导出为 tar.gz（Mac 本地，约 800MB）
#   3. make upload SERVER=user@1.2.3.4 DIR=/opt/lang-app  # SCP 到服务器
#   4. 服务器上：
#      cd /opt/lang-app
#      make load-all          # docker load 全部镜像
#      make up-prod           # compose up -d
#
# 单个镜像：
#   make build-service        # 只构建 version-service
#   make export-service       # 只导出 version-service

PLATFORM ?= linux/amd64
REPO_ROOT := $(abspath ..)
OUT_DIR := dist-images

# ---------- 镜像标签 ----------
TAG ?= prod
IMG_SERVICE = lang-service:$(TAG)
IMG_NGINX   = lang-nginx:$(TAG)
IMG_PG      = pgvector/pgvector:pg17
IMG_MIGRATE = migrate/migrate:v4.18.3

# ---------- 构建 ----------
.PHONY: build-all build-service build-nginx
build-all: build-service build-nginx
	@echo "✅ 全部镜像构建完成（platform=$(PLATFORM)）"

build-service:
	@echo "🔨 构建 $(IMG_SERVICE) ..."
	docker buildx build \
		--platform $(PLATFORM) \
		-t $(IMG_SERVICE) \
		-f $(REPO_ROOT)/server/Dockerfile \
		--load \
		$(REPO_ROOT)

build-nginx:
	@echo "🔨 拉取 $(IMG_NGINX) 基础镜像 ..."
	docker pull --platform $(PLATFORM) nginx:1.27-alpine
	docker tag nginx:1.27-alpine $(IMG_NGINX)

# ---------- 基础镜像 ----------
.PHONY: pull-base
pull-base:
	@echo "🔨 拉取第三方基础镜像（$(PLATFORM)）..."
	docker pull --platform $(PLATFORM) $(IMG_PG)
	docker pull --platform $(PLATFORM) $(IMG_MIGRATE)

# ---------- 导出 ----------
.PHONY: export-all export-service export-nginx
export-all: pull-base
	@mkdir -p $(OUT_DIR)
	@echo "📦 导出全部镜像到 $(OUT_DIR)/ ..."
	docker save -o $(OUT_DIR)/lang-images-$(TAG)-amd64.tar \
		$(IMG_SERVICE) $(IMG_NGINX) $(IMG_PG) $(IMG_MIGRATE)
	gzip -f $(OUT_DIR)/lang-images-$(TAG)-amd64.tar
	@echo "✅ $(OUT_DIR)/lang-images-$(TAG)-amd64.tar.gz ($$(du -sh $(OUT_DIR)/lang-images-$(TAG)-amd64.tar.gz | cut -f1))"

# ---------- 上传 ----------
.PHONY: upload
upload:
	@if [ -z "$(SERVER)" ]; then echo "❌ 用法: make upload SERVER=user@1.2.3.4 DIR=/opt/lang-app"; exit 1; fi
	@if [ -z "$(DIR)" ]; then echo "❌ 用法: make upload SERVER=user@1.2.3.4 DIR=/opt/lang-app"; exit 1; fi
	@echo "📤 SCP 到 $(SERVER):$(DIR) ..."
	scp -r $(OUT_DIR) $(SERVER):$(DIR)/
	scp docker-compose.prod.yml $(SERVER):$(DIR)/docker-compose.yml
	scp .env.prod $(SERVER):$(DIR)/.env.prod
	scp -r nginx $(SERVER):$(DIR)/nginx
	scp migrations $(SERVER):$(DIR)/migrations
	@echo "✅ 上传完成，SSH 到服务器后执行: cd $(DIR) && make load-all && make up-prod"

# ---------- 服务器端 ----------
# 这些目标要在服务器上执行（deploy 目录也要 Makefile）
.PHONY: load-all up-prod down-prod logs
load-all:
	@echo "📥 docker load 全部镜像 ..."
	docker load < dist-images/lang-images-$(TAG)-amd64.tar.gz
	@echo "✅ 加载完成"

up-prod:
	docker compose -f docker-compose.yml --env-file .env.prod up -d
	@echo "✅ 启动完成"

down-prod:
	docker compose -f docker-compose.yml down

logs:
	docker compose -f docker-compose.yml logs -f --tail=200

# ---------- 清理 ----------
clean:
	rm -rf $(OUT_DIR)
	docker rmi $(IMG_SERVICE) $(IMG_NGINX) 2>/dev/null || true
	@echo "✅ 清理完成"
