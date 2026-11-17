CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS rag_chunks (
    chunk_id    BIGSERIAL PRIMARY KEY,
    lang        TEXT NOT NULL,
    level       TEXT,
    chunk_type  TEXT NOT NULL,
    lemma       TEXT,
    title       TEXT NOT NULL,
    content     TEXT NOT NULL,
    source_path TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    embedding   vector(1024),
    create_version TEXT,
    updated_at  TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_rag_chunks_filter
    ON rag_chunks (lang, chunk_type, level);
-- HNSW 索引在首批数据导入后由 builder 创建，避免批量写入期间 maintenance_work_mem 尖峰

CREATE TABLE IF NOT EXISTS build_versions (
    version   TEXT PRIMARY KEY,
    lang      TEXT,
    db_url    TEXT,
    vector_snapshot_url TEXT,
    db_md5    TEXT,
    vector_md5 TEXT,
    status    TEXT,
    source_commit TEXT,
    created_at TIMESTAMPTZ DEFAULT now()
);

-- 兼容旧库：补充构建来源 commit 字段
ALTER TABLE build_versions ADD COLUMN IF NOT EXISTS source_commit TEXT;

CREATE TABLE IF NOT EXISTS build_logs (
    log_id    BIGSERIAL PRIMARY KEY,
    version   TEXT,
    stage     TEXT,
    message   TEXT,
    level     TEXT,
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS listening_jobs (
    job_id    TEXT PRIMARY KEY,
    exam_id   TEXT NOT NULL,
    lang      TEXT NOT NULL DEFAULT 'de',
    source_url TEXT NOT NULL,
    status    TEXT NOT NULL,           -- pending / running / built / failed
    manifest_path TEXT,                -- 成功后填相对路径
    log_path   TEXT,
    error_msg  TEXT,
    last_log_at TIMESTAMPTZ,           -- 最后一条 builder 日志时间，判断是否真在跑
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);
-- 增量兼容：已有库补列
ALTER TABLE listening_jobs ADD COLUMN IF NOT EXISTS last_log_at TIMESTAMPTZ;
ALTER TABLE listening_jobs ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE listening_jobs ADD COLUMN IF NOT EXISTS has_answers BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS listening_job_logs (
    log_id    BIGSERIAL PRIMARY KEY,
    job_id    TEXT NOT NULL,
    stage     TEXT,
    level     TEXT,
    message   TEXT,
    created_at TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_listening_job_logs_job ON listening_job_logs(job_id, log_id);

-- 关卡配置：管理员维护学习关卡，每个关卡关联多个听力 exam。
CREATE TABLE IF NOT EXISTS levels (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    lang        TEXT NOT NULL DEFAULT 'de',
    difficulty  TEXT NOT NULL DEFAULT 'A1',
    description TEXT,
    sort_order  INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);

ALTER TABLE levels ADD COLUMN IF NOT EXISTS difficulty TEXT NOT NULL DEFAULT 'A1';
ALTER TABLE levels ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS level_exams (
    id          BIGSERIAL PRIMARY KEY,
    level_id    BIGINT NOT NULL REFERENCES levels(id) ON DELETE CASCADE,
    exam_id     TEXT NOT NULL,
    sort_order  INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ DEFAULT now(),
    UNIQUE(level_id, exam_id)
);
CREATE INDEX IF NOT EXISTS idx_level_exams_level ON level_exams(level_id, sort_order);
