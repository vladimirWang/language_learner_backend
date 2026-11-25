# deploy/DEPLOY.md —— Ubuntu 24.04 服务器部署指南

## 架构

```
Mac (arm64)  ──  buildx --platform linux/amd64  ──►  Docker Hub  ──►  Ubuntu 24.04 (amd64)
                         │                          (按层复用)            │
                    docker push                                   docker compose pull
                                                                        │
                                                              docker compose up -d
```

默认仓库：`docker.io/furchtlos`（`lang-service` / `lang-nginx` / `lang-pg` / `lang-migrate`）。
离线备用仍可走 tar 上传：`make export-all` + `DEPLOY_MODE=tar ./deploy_all.sh`。

---

## Step 0：服务器前置检查（Ubuntu 24.04）

```bash
# 架构确认
uname -m                  # 预期：x86_64

# 内存/磁盘（version-service 要 2G，建议服务器 ≥ 4G RAM + 20G 磁盘）
free -h && df -h /

# Docker（Ubuntu 24.04 默认源里的版本就够）
docker --version          # 没有就走下面的安装
docker compose version    # 必须 v2+（docker compose 不是 docker-compose）
```

### Docker 安装（Ubuntu 24.04）

```bash
# 卸载旧版本
sudo apt remove -y docker docker-engine docker.io containerd runc

# 安装依赖
sudo apt update && sudo apt install -y ca-certificates curl gnupg

# 添加 Docker 官方 GPG key
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | \
  sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg

# 添加源（Ubuntu 24.04 = noble）
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
  https://download.docker.com/linux/ubuntu noble stable" | \
  sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

# 安装 Docker + Compose v2
sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io \
  docker-buildx-plugin docker-compose-plugin

# 验证
sudo docker compose version   # 预期：Docker Compose version v2.x.y

# （可选）当前用户免 sudo 用 docker
sudo usermod -aG docker $USER
newgrp docker
```

### TLS 证书

```bash
# 方案 A：certbot 申请 Let's Encrypt 免费证书
sudo apt install -y certbot
# 80 端口需要先停掉 nginx（还没部署也没关系）
sudo certbot certonly --standalone -d mp.hetou.vip
# 证书路径：/etc/letsencrypt/live/mp.hetou.vip/

# 拷贝到 deploy/nginx/certs/
mkdir -p /opt/lang-app/nginx/certs
sudo cp /etc/letsencrypt/live/mp.hetou.vip/fullchain.pem \
       /etc/letsencrypt/live/mp.hetou.vip/privkey.pem \
       /opt/lang-app/nginx/certs/
sudo chmod 644 /opt/lang-app/nginx/certs/*.pem

# certbot 自动续期已经装好了（systemd timer），不用管
certbot certificates   # 查看
```

---

## Step 1：Mac 本地构建打包

```bash
cd language_learner_backend/deploy

# 首次：登录镜像仓库（Token 不要写进仓库；本机执行一次即可）
docker login -u furchtlos

# 构建并推送 service/nginx/base 镜像（amd64）
make push-all
# 推送两个 tag：lang-service:prod 和 lang-service:prod-<时间戳>，后者用于回滚

# 同步部署文件并让服务器拉取镜像 + 启动
./deploy_all.sh root@your-server-ip /opt/lang-app
```

`make push-all` 的镜像分三层，重依赖只在首次或变更时构建：

| 镜像 | 内容 | 何时重建 |
|---|---|---|
| `lang-python-base:py312` | apt（ffmpeg/tesseract/git/openssh）+ uv + builder 依赖 | 仅 `Dockerfile.base` / `pyproject.toml` 变化 |
| `lang-listen-parser:prod` | PyInstaller 听力解析二进制 | 仅 `builder/` 或 `pyproject.toml` 变化 |
| `lang-service:prod` | Go 二进制 + 业务代码，`FROM` 上面两个 | 每次 `push-all`（通常 1 分钟内） |

- 首次构建 `lang-python-base` 在 Apple Silicon 上走 QEMU 模拟 amd64，apt 安装约 10~15 分钟，属正常；后续命中缓存秒级完成。
- 本地已有这两个镜像时，`push-base-image` / `push-parser-image` 只执行 `docker push`，不会重建。
- 如果本地构建缓存被清理导致又要重装 apt，可在服务器上原生构建后推送：
  `docker buildx build --platform linux/amd64 -t furchtlos/lang-python-base:py312 -f server/Dockerfile.base --push .`
- 磁盘空间不足会频繁淘汰构建缓存（`docker builder prune` 前后留意 `df -h`）。

`deploy_all.sh` 默认走仓库模式，只同步 `deploy/content/builder` 等挂载文件，
镜像由服务器 `docker compose pull` 获取，不再上传 tar 包。

旧 tar 流程（离线备用，按镜像分包）：

```bash
make build-all
make export-all     # 生成 lang-service-*.tar.gz 和 lang-base-*.tar.gz
DEPLOY_MODE=tar ./deploy_all.sh root@your-server-ip /opt/lang-app
```

分包后 `lang-base`（nginx/pgvector/migrate）通常只在首次上传，
后续只传 `lang-service`；未变化的包会自动跳过上传和 `docker load`。

或者手动一步步：

```bash
# 前端产物也一起打
cd language_learner_frontend && pnpm build

# 上传所有部署文件
scp -r dist root@your-server-ip:/opt/lang-app/admin-dist
scp -r language_learner_backend/deploy/ \
      root@your-server-ip:/opt/lang-app/
scp -r language_learner_backend/content/ \
      root@your-server-ip:/opt/lang-app/content
scp -r language_learner_backend/builder/builder/ \
      root@your-server-ip:/opt/lang-app/builder
```

---

## Step 2：服务器端拉取 + 启动

```bash
# 私有仓库需要先登录一次（公有仓库可跳过）
docker login -u furchtlos

cd /opt/lang-app

# 准备环境变量（软链接让 docker compose 自动识别）
ln -sf .env.prod .env

# 🔴 上线前必须替换 .env.prod 里的 ████████ 标记项：
#    PG_PASSWORD / SSO_CLIENT_SECRET / SSO_BASE_URL
vim .env.prod

# 从镜像仓库拉取最新镜像
docker compose -f docker-compose.prod.yml pull

# 确认镜像都在了
docker images | grep -E "furchtlos|pgvector|migrate"

# 启动所有服务
docker compose -f docker-compose.prod.yml up -d

# 查看启动过程
docker compose -f docker-compose.prod.yml ps
docker compose -f docker-compose.prod.yml logs -f --tail=100 version-service

# 手动跑一次 migration（compose up 自动跑，也可以手动确认）
docker compose -f docker-compose.prod.yml run --rm migrate

# 手动跑一次 builder（生产上应该由外部调度）
docker compose -f docker-compose.prod.yml run --rm builder --lang de
```

---

## Step 3：验证

```bash
# 后端健康检查
curl http://localhost:8080/health

# nginx HTTP（管理台前端）
curl -I http://localhost:5080
# 预期：200 + text/html

# nginx HTTPS（小程序接口）
curl -vk https://localhost:5443/api/health
# 预期：200 + JSON + TLS 协商成功

# PostgreSQL（内部）
docker exec lang-postgres-prod pg_isready -U postgres -d lang_learning

# 全部容器状态
docker compose ps
# 预期：5 个 service 全是 Up（postgres/nginx/version-service/builder 可选）
# migrate 是一次性容器，跑完就退出（Exited 0 正常）
```

---

## Step 4：开机自启

Docker 服务默认开机自启，但 compose 管理的容器需要单独设重启策略。
**本项目 compose 里所有 service 都已经写了 `restart: always`**，所以只要 Docker 服务开机启动，容器就会跟着起来。

```bash
# 确认 Docker 开机自启
sudo systemctl enable docker
sudo systemctl is-enabled docker    # 预期：enabled

# 手动测试：重启 Docker 看容器会不会自动起来
sudo systemctl restart docker
docker compose ps    # 预期：全部自动恢复
```

如果服务器用的是 **systemd** 管理 compose（可选，更正规）：

```bash
sudo tee /etc/systemd/system/lang-app.service << 'EOF'
[Unit]
Description=Language Learner App
Requires=docker.service
After=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/opt/lang-app
ExecStart=/usr/bin/docker compose -f docker-compose.yml up -d
ExecStop=/usr/bin/docker compose -f docker-compose.yml down
TimeoutStartSec=300

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable lang-app
sudo systemctl start lang-app
sudo systemctl status lang-app
```

---

## Step 5：CDN / 备案 / 微信小程序

```
mp.hetou.vip DNS → A 记录 → 服务器公网 IP  ✅

HTTPS 端口 5443 要在云服务器安全组里放行 ✅
HTTP 端口 5080 如果公网不需要也可以不放 ✅

微信小程序后台 → 开发管理 → 服务器域名：
  request 合法域名： https://mp.hetou.vip
  （必须 HTTPS、必须备案、TLS 必须被微信信任）
```

---

## 日常运维速查

```bash
# 看实时日志
docker compose logs -f --tail=200 version-service
docker compose logs -f --tail=200 nginx

# 重启某个服务
docker compose restart version-service

# 进容器调试
docker exec -it lang-service-prod sh

# 重新跑 migration
docker compose run --rm migrate

# 重新跑 builder
docker compose run --rm builder --lang de

# 版本回滚：把 IMG_SERVICE 指向旧的时间戳 tag，重新 pull + up
# IMG_SERVICE=docker.io/furchtlos/lang-service:prod-20261120-1530 \
#   docker compose -f docker-compose.prod.yml up -d

# 完全停止
docker compose down

# 清理所有容器+数据卷（⚠️ 会删 PostgreSQL 数据！）
docker compose down -v
```

---

## 更新部署（改了代码后）

```bash
# Mac 端：只重打并推送 version-service，然后同步文件 + 服务器 pull
make push-service
./deploy_all.sh root@your-server-ip /opt/lang-app
```

回滚到上一个镜像：

```bash
IMG_SERVICE=docker.io/furchtlos/lang-service:prod-20261120-1530 \
  ./deploy_all.sh root@your-server-ip /opt/lang-app
```
