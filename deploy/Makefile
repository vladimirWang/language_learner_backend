# deploy/Makefile —— Mac 本地构建 + 推送镜像仓库 + 服务器部署
#
# 默认走 Docker Hub 仓库（可用 REGISTRY/NAMESPACE 覆盖）：
#   make push-all                          # 构建并推送 service/nginx/base 镜像
#   ./deploy_all.sh root@host /opt/lang-app # 服务器 pull + up
#
# 镜像仓库需要先登录（一次性）：
#   docker login -u <账号>
#
# 旧的 tar 上传方式仍保留：make export-all / make upload（离线备用）

PLATFORM ?= linux/amd64
REPO_ROOT := $(abspath ..)
OUT_DIR := dist-images

# ---------- 镜像标签 ----------
TAG ?= prod
STAMP ?= $(shell date +%Y%m%d-%H%M)
TAG_VERSIONED = $(TAG)-$(STAMP)

# 镜像仓库：默认 Docker Hub，部署到其他仓库时改这两个变量
REGISTRY ?= docker.io
NAMESPACE ?= furchtlos

IMG_SERVICE           = $(REGISTRY)/$(NAMESPACE)/lang-service:$(TAG)
IMG_SERVICE_VERSIONED = $(REGISTRY)/$(NAMESPACE)/lang-service:$(TAG_VERSIONED)
IMG_NGINX             = $(REGISTRY)/$(NAMESPACE)/lang-nginx:$(TAG)
IMG_PG                = $(REGISTRY)/$(NAMESPACE)/lang-pg:pg17
IMG_MIGRATE           = $(REGISTRY)/$(NAMESPACE)/lang-migrate:v4.18.3

# 上游基础镜像
UPSTREAM_NGINX   = nginx:1.27-alpine
UPSTREAM_PG      = pgvector/pgvector:pg17
UPSTREAM_MIGRATE = migrate/migrate:v4.18.3

# 仅当本地缺少目标架构的镜像时才拉取：Docker Hub 偶发 EOF/限流时，
# 本地已有镜像仍可正常 tag + push，避免 make push-all 因无关的 pull 失败。
# 用法：$(call ensure_image,nginx:1.27-alpine)
define ensure_image
	@if [ "$$(docker image inspect $(1) --format '{{.Architecture}}' 2>/dev/null)" != "$(patsubst linux/%,%,$(PLATFORM))" ]; then \
		echo "⬇️  拉取 $(1)（$(PLATFORM)）..."; \
		docker pull --platform $(PLATFORM) $(1) || { \
			echo "❌ 拉取 $(1) 失败。可先在 Docker Hub 可达的网络下执行 make pull-base，或配置镜像加速器后重试"; \
			exit 1; \
		}; \
	else \
		echo "✅ 本地已有 $(1)（$(PLATFORM)），跳过拉取"; \
	fi
endef

# 旧 tar 流程使用的本地镜像名
LEGACY_IMG_SERVICE = lang-service:$(TAG)
LEGACY_IMG_NGINX   = lang-nginx:$(TAG)

# ---------- 构建 ----------
.PHONY: build-all build-service build-nginx
build-all: build-service build-nginx
	@echo "✅ 全部镜像构建完成（platform=$(PLATFORM)）"

build-service:
	@echo "🔨 构建 $(IMG_SERVICE) ..."
	docker buildx build \
		--platform $(PLATFORM) \
		-t $(LEGACY_IMG_SERVICE) \
		-t $(IMG_SERVICE) \
		-t $(IMG_SERVICE_VERSIONED) \
		-f $(REPO_ROOT)/server/Dockerfile \
		--load \
		$(REPO_ROOT)

build-nginx:
	@echo "🔨 准备 $(IMG_NGINX) 基础镜像 ..."
	$(call ensure_image,$(UPSTREAM_NGINX))
	docker tag $(UPSTREAM_NGINX) $(LEGACY_IMG_NGINX)
	docker tag $(UPSTREAM_NGINX) $(IMG_NGINX)

# ---------- 推送镜像仓库 ----------
.PHONY: push-all push-service push-nginx push-base
push-all: push-service push-nginx push-base
	@echo "✅ 镜像已推送：$(IMG_SERVICE) / $(IMG_SERVICE_VERSIONED)"

push-service:
	@echo "🚀 构建并推送 $(IMG_SERVICE) + $(IMG_SERVICE_VERSIONED) ..."
	docker buildx build \
		--platform $(PLATFORM) \
		-t $(IMG_SERVICE) \
		-t $(IMG_SERVICE_VERSIONED) \
		-f $(REPO_ROOT)/server/Dockerfile \
		--push \
		$(REPO_ROOT)

push-nginx:
	@echo "🚀 镜像并推送 $(IMG_NGINX) ..."
	$(call ensure_image,$(UPSTREAM_NGINX))
	docker tag $(UPSTREAM_NGINX) $(IMG_NGINX)
	docker push $(IMG_NGINX)

push-base:
	@echo "🚀 镜像并推送基础镜像（pgvector / migrate）..."
	$(call ensure_image,$(UPSTREAM_PG))
	docker tag $(UPSTREAM_PG) $(IMG_PG)
	docker push $(IMG_PG)
	$(call ensure_image,$(UPSTREAM_MIGRATE))
	docker tag $(UPSTREAM_MIGRATE) $(IMG_MIGRATE)
	docker push $(IMG_MIGRATE)

# ---------- 旧 tar 流程（离线备用，按镜像拆包） ----------
.PHONY: pull-base export-all export-service export-base
pull-base:
	@echo "🔨 拉取第三方基础镜像（$(PLATFORM)）..."
	docker pull --platform $(PLATFORM) $(UPSTREAM_NGINX)
	docker pull --platform $(PLATFORM) $(UPSTREAM_PG)
	docker pull --platform $(PLATFORM) $(UPSTREAM_MIGRATE)
	docker tag $(UPSTREAM_NGINX) $(LEGACY_IMG_NGINX)

export-service:
	@mkdir -p $(OUT_DIR)
	@echo "📦 导出 lang-service ..."
	docker save -o $(OUT_DIR)/lang-service-$(TAG)-amd64.tar $(LEGACY_IMG_SERVICE)
	gzip -f $(OUT_DIR)/lang-service-$(TAG)-amd64.tar
	@echo "✅ $(OUT_DIR)/lang-service-$(TAG)-amd64.tar.gz ($$(du -sh $(OUT_DIR)/lang-service-$(TAG)-amd64.tar.gz | cut -f1))"

export-base: pull-base
	@mkdir -p $(OUT_DIR)
	@echo "📦 导出基础镜像（nginx / pgvector / migrate）..."
	docker save -o $(OUT_DIR)/lang-base-$(TAG)-amd64.tar \
		$(LEGACY_IMG_NGINX) $(UPSTREAM_PG) $(UPSTREAM_MIGRATE)
	gzip -f $(OUT_DIR)/lang-base-$(TAG)-amd64.tar
	@echo "✅ $(OUT_DIR)/lang-base-$(TAG)-amd64.tar.gz ($$(du -sh $(OUT_DIR)/lang-base-$(TAG)-amd64.tar.gz | cut -f1))"

export-all: export-service export-base
	@echo "✅ 分包导出完成（service 每次重建，base 通常只传一次）"

# ---------- 上传（旧 tar 流程） ----------
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
.PHONY: load-all pull up-prod down-prod logs
load-all:
	@echo "📥 docker load 镜像（-q 不打印重复 layer 进度）..."
	@if [ -f dist-images/lang-base-$(TAG)-amd64.tar.gz ]; then docker load -q < dist-images/lang-base-$(TAG)-amd64.tar.gz; fi
	@if [ -f dist-images/lang-service-$(TAG)-amd64.tar.gz ]; then docker load -q < dist-images/lang-service-$(TAG)-amd64.tar.gz; fi
	@if [ -f dist-images/lang-images-$(TAG)-amd64.tar.gz ]; then docker load -q < dist-images/lang-images-$(TAG)-amd64.tar.gz; fi
	@echo "✅ 加载完成"

pull:
	docker compose -f docker-compose.prod.yml --env-file .env.prod pull

up-prod:
	docker compose -f docker-compose.prod.yml --env-file .env.prod up -d
	@echo "✅ 启动完成"

down-prod:
	docker compose -f docker-compose.prod.yml --env-file .env.prod down

logs:
	docker compose -f docker-compose.prod.yml --env-file .env.prod logs -f --tail=200

# ---------- 清理 ----------
clean:
	rm -rf $(OUT_DIR)
	docker rmi $(LEGACY_IMG_SERVICE) $(LEGACY_IMG_NGINX) 2>/dev/null || true
	@echo "✅ 清理完成"
