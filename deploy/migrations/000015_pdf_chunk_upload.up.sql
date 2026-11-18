-- PDF 分片上传会话：支持断点续传，合并完成后再创建导入任务并投递 MQ。
CREATE TABLE IF NOT EXISTS pdf_upload_sessions (
    upload_id    TEXT PRIMARY KEY,
    fingerprint  TEXT NOT NULL UNIQUE,
    original_name TEXT NOT NULL,
    size_bytes   BIGINT NOT NULL,
    lang         TEXT NOT NULL DEFAULT 'de',
    chunk_size   INTEGER NOT NULL,
    total_chunks INTEGER NOT NULL,
    status       TEXT NOT NULL DEFAULT 'uploading', -- uploading/completed
    job_id       TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS pdf_upload_chunks (
    upload_id   TEXT NOT NULL REFERENCES pdf_upload_sessions(upload_id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL,
    size_bytes  BIGINT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (upload_id, chunk_index)
);
