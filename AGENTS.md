# 项目背景与 AI 协作上下文（AGENTS.md）

> 本文件供 TraeCN（及其他 AI 编码助手）快速理解项目定位、架构与约束。
> 完整实现细节见同目录 `language_learn/方案.md`；本文件只保留背景、决策与约定，不重复方案中的代码配置。

## 1. 项目一句话

面向 PC 端的**离线多语种外语学习 Agent**（德语 / 英语 / 日语，入门到 B1 / N5-N3）：本地小模型负责理解与编排，结构化真值库 + 本地向量库提供知识素材；服务端只做素材构建、向量入库与版本分发，不在云端跑模型。

## 2. 目标与用户

- **目标用户**：自学德语、英语、日语的学习者；客户端在个人 PC 上离线使用。
- **核心能力**：单词卡片、变位 / 变形查询、语法辨析、例句讲解、真题练习、间隔复习、客观题判分。
- **设计目标**：
  - 离线可用：断网时学习、查询、做题不依赖云端推理。
  - 低幻觉：变位、变形、释义等标准答案来自结构化真值表，模型不自行编造。
  - 低资源：服务端部署在 **2 核 2G** 云主机（构建期可用内存余量约 400MB），必须严控常驻内存。
  - 可扩展：新增语种（如法语、韩语）只加语种插件与素材，不改主流程。

## 3. 要解决的核心问题

1. **小模型容易记错语法**：德语三四格、名词词性复数、动词支配格，日语动词活用等细碎规则，靠模型参数记忆极易混淆。
2. **自由文本检索不可靠**：纯 Markdown 进向量库，语义召回存在噪声与错配；精确取值需要确定性查询。
3. **服务端内存紧张**：2C2G 上不能常驻模型、重型向量库或任务队列；Python 解析任务必须跑完即退出。
4. **多语种素材混杂**：跨语种召回会把德语规则套到日语上，需要在数据层强制语种隔离。

## 4. 架构分层（职责边界）

| 层 | 运行位置 | 技术 | 性质 | 职责 |
|---|---|---|---|---|
| Agent 大脑 | PC 本地 | 本地蒸馏小模型（GGUF） | 本地进程 | 意图理解、工具选择、基于检索素材组织回答；不做真值来源 |
| 本地真值库 | PC 本地 | SQLite `ground_truth.db` | 只读预置文件 | 单词、变位、词性、支配格等结构化真值，SQL 精确查询 |
| 本地向量库 | PC 本地 | Qdrant Embedded / Milvus Lite / Chroma | 嵌入进程 | MD 讲解、辨析、例句、真题切片的离线语义检索 |
| 用户进度库 | PC 本地 | SQLite `user_data.db` | 本地读写 | 学习记录、错题、熟练度、复习进度；不上传服务端 |
| 版本服务 | 云主机 | Go（单二进制） | 常驻 | 版本清单、素材变更触发、下载签名链接、内存水位保护、串行调度 |
| 构建任务 | 云主机 | Python（uv 管理依赖） | **一次性子进程，跑完退出** | MD/CSV/PDF 解析、切片、调外部 Embedding API、写 pgvector、导出快照、上传对象存储 |
| 数据库 | 云主机 Docker | PostgreSQL 17 + pgvector | 常驻容器 | 构建期 chunk、向量、版本记录、构建日志；客户端运行时不连此库 |
| 对象存储 | 云主机 / OSS | S3 兼容 | — | 存放 `ground_truth.db`、向量快照、素材包 |

### 关键数据流

```
素材源（CSV + MD + PDF + index）→ Git/管理系统推送
  → Go 版本服务（水位校验 + 串行锁）
  → Python 一次性 Job（解析 → embedding API → pgvector → 导出快照 → 上传 OSS）
  → 对象存储（按 lang 分目录）
  → 客户端版本比对、下载、md5 校验、本地加载
```

## 5. 技术栈与选型理由

| 组件 | 选型 | 理由 |
|---|---|---|
| 版本服务 | Go 1.23，标准库 `net/http`（或轻量路由） | 无 GC 抖动、单二进制、常驻内存 < 20MB |
| 构建任务 | Python 3.12 + **uv** | 文本/PDF/NLP 生态成熟；任务退出即释放全部内存 |
| 包源 | 清华镜像 `https://pypi.tuna.tsinghua.edu.cn/simple` | 国内拉取稳定；uv 项目级配置 |
| 数据库 | PostgreSQL 17 + pgvector | 元数据 SQL join 与向量检索同库；复用现有公共 PG |
| 向量引擎（客户端） | 嵌入式（Qdrant Embedded / Milvus Lite / Chroma） | 不要求用户本机启动数据库服务 |
| Embedding | 外部多语言 Embedding API | 禁止在 2C2G 主机本地加载模型 |
| PDF 解析 | PyMuPDF | 比 pdfplumber 内存友好，逐页流式解析 |
| 容器 | Docker Compose，全部官方镜像 | `pgvector/pgvector:pg17`、`golang:1.23-alpine`、`alpine:3.20`、`python:3.12-slim` |

### 明确不引入

- 服务端 LLM / 本地 Embedding 模型（torch、transformers、sentence-transformers）
- Milvus Standalone / Distributed、Elasticsearch、常驻 Redis 队列、Celery / RQ worker
- 客户端直连服务端 PG（运行时只用本地 SQLite + 本地向量快照）

## 6. 目录规划（TraeCN 工程根目录）

```
language_learn/
├── AGENTS.md                         # 本文件：项目背景与协作上下文
├── 方案.md                           # 完整实现方案（架构、表结构、Compose、Dockerfile、流水线）
├── server/                           # Go 常驻版本服务
│   ├── cmd/version-service/main.go
│   ├── internal/{handler,scheduler,runner,store}
│   ├── go.mod
│   └── Dockerfile
├── builder/                          # Python 一次性构建任务（uv）
│   ├── pyproject.toml
│   ├── uv.lock
│   ├── scripts/{build_ground_truth,build_rag,export_snapshot,sync_index}.py
│   └── Dockerfile
├── content/                          # 素材源（唯一事实源，Git 管理）
│   ├── de/  en/  ja/                 # 按语种分 grammar / vocab / csv
│   ├── exams/                        # PDF 真题（按语种分目录）
│   └── grammar.index.md             # 清单：路径 + lang + 标签 + level + hash
├── admin/                            # 管理台前端（React 19 + Vite 8 + antd，BrowserRouter）
├── deploy/
│   ├── docker-compose.dev.yml        # dev：复用外部 PG，env=.env.dev
│   ├── docker-compose.prod.yml       # prod：独立 PG + pgBouncer，env=.env.prod
│   ├── .env.dev / .env.prod
│   └── postgres/init/01-pgvector.sql
└── docs/
```

## 7. 关键设计约束（写代码时必须遵守）

1. **真值与讲解分离**
   - 结构化表（SQL/CSV）：精确取值、判分、出题；
   - MD 切片 + 向量检索：用法、辨析、例句、场景；
   - 模型只能基于工具返回内容输出，禁止自行生成变位 / 变形。

2. **语种强隔离**
   - 所有表与向量 chunk 带 `lang`（`de` / `en` / `ja`）；
   - SQL 与向量检索必须前置 `lang` 过滤；级别体系：de/en 用 CEFR（A1–B2），ja 用 JLPT（N5–N1）。

3. **Python 非常驻**
   - Builder 由 Go 触发拉起，执行完毕进程退出；
   - 同一时刻只允许一个构建任务；Go 侧做内存水位拦截（阈值 120MB）。

4. **数据库迁移与写入归属**
   - DDL 只由 `deploy/migrations` 下的 golang-migrate 执行；Go / Python 运行期都不建表、不改表。
   - 所有表间关联统一使用**逻辑外键**，禁止新增数据库物理 FOREIGN KEY 约束；主键、唯一约束和查询索引仍必须保留。
   - 写入方必须在 Go 事务中校验父记录存在；删除方必须在同一事务中显式删除子记录，或在存在子记录时拒绝删除，不得依赖数据库级联。
   - 每张表只有一个写入方：
     | 表 | 写入方 |
     |---|---|
     | `listening_jobs`、`listening_job_logs`、`levels`、`level_exams`、`speaking_items` | Go |
     | `build_versions`、`build_logs` | Go（唯一写入） |
     | `rag_chunks`（含向量） | Python builder |
   - Builder 不直接写 `build_versions` / `build_logs`：结束时输出 `BUILD_RESULT {json}`，日志走 stdout，由 Go scheduler 解析入库；进程异常时 Go 用 `MarkFailed` 兜底。

4. **库分离**
   - `ground_truth.db`（只读预置）与 `user_data.db`（私有读写）物理分库；版本更新不覆盖用户进度。

5. **内存限制（prod 容器）**
   - PG 512m / Go 128m / Builder 384m；PG 压配 `shared_buffers=64MB`、`max_connections=16`，前置 pgBouncer。

6. **素材增量**
   - 以 `grammar.index.md` 的文件 hash 做增量比对：新增切片入库、修改先删旧 chunk 再重建、删除同步清理。

7. **语种特有属性**
   - 主表只放通用字段；日语音读训读、动词分类、英语时态变形等放入 `extra_json`，不污染主表结构。

## 8. 客户端 Agent 工具路由（运行在 PC 本地）

| 请求类型 | 工具 | 数据来源 |
|---|---|---|
| 变位 / 词性 / 支配格 / 抽词出题 / 客观题判分 | SQLite SQL 精确查询 | `ground_truth.db` |
| 用法、搭配、易混辨析、例句、真题讲解 | 向量语义检索（带 lang 过滤） | 本地向量快照 |
| 复习待办、错题重练 | 进度查询 | `user_data.db` |
| 无关语法的闲聊 | 模型直接回答 | 无工具调用 |

语种规则封装为独立插件（`plugin_de` / `plugin_en` / `plugin_ja`），避免业务代码散落 `if/else`。

## 9. 术语表

- **ground_truth.db**：客户端只读 SQLite，存放单词与语法变位标准答案。
- **user_data.db**：客户端私有 SQLite，存放学习进度与笔记。
- **chunk**：MD / PDF 按标题或语义切分后的最小检索单元，带 metadata 与向量。
- **向量快照**：构建完成后导出、供客户端嵌入式向量引擎离线加载的静态文件。
- **pgBouncer**：PG 连接池，把多连接收敛为少量真实后端进程，控制 PG 内存。
- **Embedding API**：外部多语言向量服务；服务端与客户端都不在本地加载模型。
- **增量更新**：客户端比对版本号与文件 md5，只下载变更的真值库 / 向量快照。

## 10. 当前进度与待确认项

### 进度

- 架构方案、数据模型、Compose / Dockerfile / uv 配置、流水线与路由规则已在 `方案.md` 中定稿。
- 工程代码已落地：`server/`（Go 版本服务）、`builder/`（Python 一次性构建任务，入口 `python -m builder`）、`content/`（三语种样例素材与索引）、`deploy/`（dev/prod compose、env、PG 初始化 SQL）。
- 本地已通过：`go build` / `go vet`、`uv lock` / `uv sync --locked`、三语种真值库与切片冒烟测试、假 Embedding + 测试库的全流水线、版本服务四个 API 联调。

### 待确认

1. Embedding API 的向量维度（当前默认 1024，改 `EMBEDDING_DIM` 时需同步 `rag_chunks.embedding` 定义）。
2. dev 外部 PG 已按实际 infra 对齐：`127.0.0.1:15432`、用户 `postgres`、库 `lang_learning_dev`（容器内用 `host.docker.internal`）。
3. 对象存储选型与预签名下载接口权限（Go 已用 minio-go 预签名；dev 留空时产物保留本地）。
4. 客户端嵌入式向量引擎最终选型（Qdrant Embedded / Milvus Lite / Chroma），决定快照导出格式（当前为引擎无关的 `vectors.jsonl` + `manifest.json`）。
5. 客户端增量更新是否需要差分补丁（当前方案为整文件下载 + md5 校验）。
6. 容器化部署时 version-service 无法直接 exec 宿主机 Python，构建需通过 compose `run builder` 或另行挂载构建环境触发。


## 11. 开发约定

- 提交前：Python 跑 `uv sync --locked` 并提交 `uv.lock`；Go 跑 `go mod tidy`。
- 不向 Python 依赖引入模型运行时库；不新增常驻后台进程。
- 新增语种：加 `content/<lang>/` 素材、`lang` 取值、对应语种插件，主流程与表结构不改。
- 配置一律走环境变量 / `.env`，密钥不写入代码仓库。

### 小程序与隐私约定

- **小程序当前只承载德语学习**：不在界面提供语种切换；「我的」页不展示语种字段，德语等级只提供 CEFR `A1–C2`。新增语种通过服务端配置与新客户端入口引入，不在德语页面暴露语种。
- **服务端保持多语种能力，不写死德语**：缺省语种与允许集合分别由 `DEFAULT_LANG`、`SUPPORTED_LANGS` 环境变量配置；学员端接口保留 `lang` 参数，未传时使用 `DEFAULT_LANG`，只接受 `SUPPORTED_LANGS` 内的值。新增语种只改配置与素材，不改接口契约。
- **隐私标识不下发、不展示**：小程序任何界面、以及服务端返回给小程序的用户对象，都不得包含 user id、openid、unionid 等身份标识；这些字段仅用于服务端鉴权与内部存储，不进入客户端展示层。
- **语种不在学员端界面展示**：`lang` 只用于服务端筛选、鉴权与内部存储，**不在 desktop client 与小程序任何界面元素中展示**（包括列表项、测试/任务卡片、标题、摘要与详情元信息）。接口可以保留 `lang` 供服务端内部使用，但页面不得渲染；新增多语种入口由产品另行设计，不在现有德语界面暴露。

> （注：部分内容由豆包 AI 生成）