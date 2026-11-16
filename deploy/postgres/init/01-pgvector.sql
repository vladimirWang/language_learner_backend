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
