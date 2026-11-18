# 灵瓜 · 德语专业四级（PGG）接入方案

> 产品：灵瓜（Lingua）— 桌面端德语学习应用，闯关式覆盖听说读写。
> 本文档描述如何将德专四级（PGG）真题集（PDF）解析为结构化题目与讲解素材，并接入灵瓜的听、说、读、写四大板块。
> 关联文档：`AGENTS.md`（项目背景与约束）、`外语学习Agent系统实现方案.md`（后端构建与分发）、`灵瓜歌德口试模块实现方案.md`（口语模块）。

---

## 1. 接入定位与边界

### 1.1 真题题型清单

PDF 真题集覆盖以下五类题型：

| 题型（德语） | 中文 | 归属板块 | 数据性质 |
|---|---|---|---|
| Diktat | 听写 | 听 / 写 | 真值：听写原文；素材：难点讲解 |
| Hörverstehen | 听力理解 | 听 | 真值：题干、选项、答案；素材：听力原文 |
| Leseverstehen | 阅读理解 | 读 | 真值：题干、选项、答案；素材：阅读篇章 |
| Grammatik und Wortschatz | 语法与词汇 | 读 / 写（知识题） | 真值：题干、选项、答案；素材：关联语法条目 |
| schriftlicher Ausdruck | 书面表达 | 写 | 真值：写作题干、字数要求；素材：范文与句式 |

### 1.2 两条不可逾越的边界

1. **版权红线**：PGG 真题仅限个人学习使用；解析脚本只在本地 / 私有服务端运行，**不将真题原文打包进对外分发的安装包**。对外版本只下发结构化题目与自有讲解，原文素材按合规要求隔离。
2. **两套体系隔离**：灵瓜同时承载「歌德等级（A1/A2/B1/B2）」与「德专四级（PGG）」两套体系。题库、RAG chunk、检索过滤必须带体系标签，练习路由不得混用：歌德练习不召回 PGG 素材，反之亦然。

### 1.3 数据分类原则（沿用既有架构）

- **结构化真值**（题干、选项、标准答案、搭配、变位）→ 写入 SQLite `ground_truth.db`，用于自动判分与精确查询。
- **长文本素材**（阅读篇章、听力原文、写作题干、听写文本、讲解）→ 切片后进入本地向量库（RAG），供 Agent 检索定位与讲解。
- **LLM 不做真值判定**：对错以 SQLite 查表为准，LLM 只组织讲解与反馈话术。

---

## 2. 总体流水线

```
PGG 真题 PDF（试卷册 + 答案册 / 听力原文册）
        │  Python 一次性 Job（pdfplumber / PyMuPDF，必要时 OCR）
        ▼
预处理：拆套试卷 → 按题型分块 → 清洗文本 → 输出原始 JSON 块
        ├──────────────────────┬──────────────────────┐
        ▼                      ▼                      ▼
结构化题目 CSV          长文本素材 MD            答案 / 原文绑定
（导入 SQLite 真值库） （切片入 RAG 向量库）   （按 paper_id 关联）
        │                      │
        └──────────┬───────────┘
                   ▼
          Go 后端：题目接口 / 提交判分 / 素材检索
                   ▼
        PC 客户端：听 / 读 / 写板块调用（PGG 分类入口）
```

> 流水线复用既有服务端架构：Go 常驻服务做触发与接口，Python 一次性 Job 跑解析与入库，构建产物（新 `ground_truth.db` + 向量快照）上传对象存储，客户端按版本号增量下载。

---

## 3. PDF 预处理阶段（Python）

### 3.1 工具选型

- **pdfplumber**：优先，文字与坐标提取准确，适合可复制文字版 PDF。
- **PyMuPDF（fitz）**：备选，处理版面复杂页面。
- **OCR（PaddleOCR 类）**：仅当真题为扫描图片版时启用。

### 3.2 处理步骤

1. **拆套试卷**：按目录 / 年份标识（如 `PGG2013A`、`PGG2013B`）切分为单套试卷；同时分离「试卷部分」与「答案册 / 听力原文册」。
2. **题型分块**：在页面内按坐标 + 关键词识别题型块，关键词：
   - `Diktat`、`Hörverstehen`、`Leseverstehen`、`Grammatik und Wortschatz`、`schriftlicher Ausdruck`。
3. **文本清洗**：去除页码、页眉页脚、目录文字、多余换行与无关标记；统一德语字符（`ä ö ü ß`）。
4. **输出中间产物**：每套试卷的原始 JSON 块，先不做深度解析，便于人工校对。

```json
{
  "paper_id": "pgg_2013_a",
  "exam_system": "pgg",
  "section": "Hoerverstehen",
  "raw_text": "提取到的原文块",
  "page_no": 5
}
```

### 3.3 关键关联：试卷与答案 / 听力原文绑定

试卷册只有题干；标准答案、听写原文、听力文稿在答案册 / 附录中。脚本必须通过 `paper_id`（必要时加题号）把题干与对应答案、原文绑定，否则讲解阶段无法定位素材。

### 3.4 预处理坑点

- 排版错位、换行断裂不要纯关键词硬匹配；保留人工校对入口，可修正 JSON 后再入库。
- 听力题题干在试卷册、听力文稿在附录册，两册都要解析并建立映射。

---

## 4. 五类题型的结构化设计

### 4.1 Diktat（听写）

**提取内容**：听写完整原文（来自答案册 / 听力原文册）、词数。

**存储**：

- SQLite：`diktat_id, paper_id, full_text, word_count`；
- RAG：全文切片，用于讲解难点词汇与句式。

**客户端用途**：播放音频（TTS 合成或外部音频）→ 用户打字提交 → 逐词 / 逐句比对批改，容错大小写与标点。

### 4.2 Hörverstehen（听力理解）

**提取内容**：题干、选项（A/B/C/D）、标准答案、对应听力原文。

**存储**：

- SQLite：`question_id, paper_id, section=hoeren, stem, options_json, correct_answer, audio_ref`；
- RAG：听力原文长文本单独切片；做错后 Agent 检索原文定位讲解。

**音频说明**：PDF 不含音频。两条路线：

1. 本地 Piper TTS 按统一语速 / 音色合成（适配离线桌面端）；
2. 外部音频资源，文件名与 `audio_ref` 绑定。

### 4.3 Leseverstehen（阅读理解）

**提取内容**：阅读篇章 + 配套选择题（题干 + 选项 + 答案）。

**存储**：

- SQLite：只存小题结构（题干、选项、答案）；
- RAG：整篇阅读篇章单独切片入库，支持按句子定位讲解。

> 长篇阅读不进 SQLite；SQLite 只承载可判分的小题。

### 4.4 Grammatik und Wortschatz（语法与词汇）

**提取内容**：题干、选项、标准答案；题目涉及的语法点。

**存储**：

- SQLite：题目、选项、答案（自动判分）；
- RAG：题干关联既有 `de/grammar/*.md` 语法条目（如考 `kein` 变位，关联「否定冠词」）。

### 4.5 schriftlicher Ausdruck（书面表达）

**提取内容**：写作要求、主题、字数要求。

**存储**：

- SQLite：`writing_id, paper_id, prompt, word_limit`；
- RAG：写作题干 + 范文 + 常用句式。

**批改链路**：用户提交作文 → 语法真值库（SQLite）做语法纠错 → LLM 结合 RAG 素材做内容点评；与口语批改逻辑同源。

---

## 5. 数据库设计（ground_truth.db 新增表）

### 5.1 试卷主表

```sql
CREATE TABLE exam_papers (
    paper_id     TEXT PRIMARY KEY,        -- pgg_2013_a
    exam_system  TEXT NOT NULL,           -- pgg / goethe
    level        TEXT,                    -- pgg4 / B1 ...
    year         INTEGER,
    title        TEXT,
    source_note  TEXT,                    -- 素材出处与版权备注
    create_version TEXT
);
```

### 5.2 客观题通用表（听力 / 阅读 / 语法词汇复用）

```sql
CREATE TABLE exam_questions (
    question_id    INTEGER PRIMARY KEY AUTOINCREMENT,
    paper_id       TEXT NOT NULL,
    exam_system    TEXT NOT NULL,
    section        TEXT NOT NULL,         -- hoeren / lesen / grammar_wortschatz
    q_no           TEXT,                  -- 题号
    stem           TEXT NOT NULL,
    options_json   TEXT,                  -- ["A. ...", "B. ...", ...]
    correct_answer TEXT,
    source_text_id TEXT,                  -- 关联 RAG 原文 chunk / 篇章
    grammar_ref    TEXT,                  -- 关联语法 MD 路径（可空）
    level          TEXT,
    create_version TEXT,
    FOREIGN KEY (paper_id) REFERENCES exam_papers(paper_id)
);
```

### 5.3 听写表

```sql
CREATE TABLE exam_diktat (
    diktat_id   INTEGER PRIMARY KEY AUTOINCREMENT,
    paper_id    TEXT NOT NULL,
    full_text   TEXT NOT NULL,
    word_count  INTEGER,
    audio_ref   TEXT,
    create_version TEXT,
    FOREIGN KEY (paper_id) REFERENCES exam_papers(paper_id)
);
```

### 5.4 写作题表

```sql
CREATE TABLE exam_writing (
    writing_id   INTEGER PRIMARY KEY AUTOINCREMENT,
    paper_id     TEXT NOT NULL,
    prompt       TEXT NOT NULL,
    word_limit   INTEGER,
    material_ref TEXT,                    -- 关联 RAG 范文 / 句式
    create_version TEXT,
    FOREIGN KEY (paper_id) REFERENCES exam_papers(paper_id)
);
```

### 5.5 用户答题记录（user_data.db，本地私有）

```sql
CREATE TABLE user_exam_record (
    record_id    INTEGER PRIMARY KEY AUTOINCREMENT,
    paper_id     TEXT,
    question_id   INTEGER,
    section      TEXT,
    user_answer  TEXT,
    is_correct   BOOLEAN,
    error_note   TEXT,
    answered_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

---

## 6. RAG 素材规范

### 6.1 长文本素材 MD（与语法 MD 格式统一）

每套阅读 / 听力原文单独生成 MD，头部元信息沿用全局规范：

```html
<!--
lang: de
level: pgg4
chunk_type: reading_text
exam_system: pgg
tags: pgg, 2013A, leseverstehen
source_md_path: de/exam/pgg_2013A_lese_01.md
-->

# PGG2013A 阅读原文 1

【原文文本】
```

### 6.2 元数据标签约定

| 字段 | 取值 | 用途 |
|---|---|---|
| `exam_system` | `pgg` / `goethe` | 体系隔离，检索强制过滤 |
| `section` | `diktat / hoeren / lesen / grammar / writing` | 板块路由 |
| `level` | `pgg4` / `A1`…`B2` | 难度过滤 |
| `tags` | 年份、卷别、题型 | 精确定位 |

### 6.3 索引清单

真题素材同样登记到 `grammar.index.md`（或独立 `exam.index.md`），记录路径、`exam_system`、标签、level 与文件 hash，供增量构建脚本比对新增 / 修改 / 删除。

---

## 7. 接入四大板块

### 7.1 读（Leseverstehen、Grammatik und Wortschatz）

- 客户端调用阅读 / 语法词汇选择题；
- 提交后 SQLite 查表自动判分；
- 点击「讲解」→ Agent 检索 RAG 阅读原文 + 关联语法 MD，给出定位讲解。

### 7.2 写（schriftlicher Ausdruck）

- 下发写作题干与字数要求；
- 用户提交作文 → SQLite 语法真值校验（变位、格、介词、搭配）→ LLM 结合 RAG 范文 / 句式做内容点评与改写建议；
- 批改逻辑复用口语模块的「真值校验 + LLM 组织话术」分层。

### 7.3 听（Hörverstehen、Diktat）

- 下发题目；前端播放 TTS 合成音频（或外部音频）；
- 选择题作答后自动判分；听写题逐词比对；
- 做错后检索听力原文 / 听写原文讲解。

### 7.4 说（Sprechen）

- **PGG 专四不设口语真题**，本板块不强行绑定 PGG；
- 口语练习继续沿用歌德 A1/A2/B1 口试体系，在客户端分类中与 PGG 笔试分开入口。

### 7.5 客户端分类入口

PC 端练习 Tab 分为两类：

- **歌德等级练习**：A1/A2/B1（口语 + 笔试）；
- **德专四级 PGG 真题**：听写、听力、阅读、语法词汇、写作（无口语）。

---

## 8. 后端接口设计（Go）

Go 服务只做查询、下发与判分调度，不做 PDF 解析（解析在 Python Job）。

| 接口 | 方法 | 说明 |
|---|---|---|
| `/api/exam/paper/list?system=pgg` | GET | 列出 PGG 试卷列表 |
| `/api/exam/question/{qid}` | GET | 获取单题（题干、选项、音频引用） |
| `/api/exam/answer/submit` | POST | 提交答案，SQLite 查表判分 |
| `/api/exam/material/rag` | GET | 检索本题对应原文 / 语法讲解素材 |
| `/api/exam/writing/{wid}` | GET | 获取写作题干与素材引用 |

> 离线模式下，上述数据来自本地 SQLite 与本地向量快照；服务端接口仅用于首次同步与版本更新。

---

## 9. 渐进式落地路线

### 阶段 1｜解析骨架与人工校对（1–2 周）

- 跑通 PDF → 原始 JSON 块；
- 实现试卷拆分、题型分块、文本清洗；
- 建立试卷 ↔ 答案 / 原文绑定映射；
- 人工校对一批样卷，确认解析准确率。

**验收**：一套样卷五类题型全部正确切块；题干与答案绑定无误。

### 阶段 2｜客观题入库与判分（2–3 周）

- 听力 / 阅读 / 语法词汇结构化入 SQLite；
- 实现提交判分接口与本地答题记录；
- 阅读 / 听力原文生成 MD 入 RAG。

**验收**：客观题可作答、自动判分；错题可检索到对应原文讲解。

### 阶段 3｜听写与写作接入（2–3 周）

- 听写原文入库 + TTS 音频合成 + 逐词比对；
- 写作题干下发 + 作文批改链路（真值校验 + LLM 点评）；
- 范文 / 句式素材入 RAG。

**验收**：听写可提交并批改；作文可返回语法错误清单与改写建议。

### 阶段 4｜打磨与扩充（持续迭代）

- 扩充真题套卷；优化解析容错；
- 错题本与复习推荐（关联语法 MD）；
- 音频音色 / 语速调优；
- 按合规要求评估原文素材的本地留存策略。

---

## 10. 风险与规避

| 风险 | 影响 | 规避措施 |
|---|---|---|
| 版权风险 | 真题原文违规分发 | 仅本地 / 私有解析；对外版本隔离原文；保留出处备注 |
| 题型识别不稳定 | 题目切块错误 | 坐标 + 关键词双重判断；保留人工校对 JSON 入口 |
| 题干与答案脱节 | 判分错误 | 以 `paper_id` + 题号强绑定；入库前校验答案非空 |
| 体系素材混用 | 歌德练习混入 PGG 内容 | 检索强制带 `exam_system` 过滤；RAG metadata 隔离 |
| 难度错配 | PGG 难度高于歌德 B1，超纲干扰 | 按 `level` 标签过滤，不跨体系推荐 |
| 听力无音频 | 无法练习听力 / 听写 | 本地 Piper TTS 合成，或绑定外部音频；`audio_ref` 留空时提示 |
| 写作批改幻觉 | LLM 给出错误语法判断 | 语法对错走 SQLite 真值库，LLM 只组织反馈 |

---

## 11. 与既有架构的衔接

- **真值库**：PGG 题目与答案写入同一 `ground_truth.db`，通过 `exam_system` 字段与歌德素材隔离。
- **素材流水线**：真题 MD 与语法 MD 走同一套 Python 构建 Job（切片、embedding、入 pgvector、导出快照）。
- **版本分发**：新增表与素材随版本快照下发；客户端比对 md5 增量更新。
- **用户数据**：答题记录与作文只存本地 `user_data.db`，不上传服务端。
- **语种扩展**：表结构与 metadata 已预留 `lang` 字段，后续接入英语、日语真题时复用同一套流水线。

---

## 12. 待确认项

1. 真题 PDF 是文字版还是扫描版（决定是否需要 OCR）。
2. 听力 / 听写音频来源：TTS 合成还是外部音频文件。
3. 对外版本对真题原文的留存范围（合规口径）。
4. PGG 与歌德两套体系在客户端的难度映射与推荐策略。
5. 写作批改是否需要接入云端 LLM 兜底，还是纯本地模型完成。

> （注：部分内容由豆包 AI 生成）