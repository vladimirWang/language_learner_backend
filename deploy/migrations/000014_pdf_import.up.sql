-- PDF 试卷导入任务：文件先落盘并登记到数据库，再投递 RabbitMQ 异步解析。
CREATE TABLE IF NOT EXISTS pdf_import_jobs (
    job_id       TEXT PRIMARY KEY,
    original_name TEXT NOT NULL,
    stored_path  TEXT NOT NULL,
    size_bytes   BIGINT NOT NULL DEFAULT 0,
    file_md5     TEXT NOT NULL DEFAULT '',
    lang         TEXT NOT NULL DEFAULT 'de',
    status       TEXT NOT NULL DEFAULT 'pending', -- pending/queued/running/succeeded/failed
    message      TEXT NOT NULL DEFAULT '',
    page_count   INTEGER NOT NULL DEFAULT 0,
    result_json  JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_pdf_import_jobs_created ON pdf_import_jobs (created_at DESC);

CREATE TABLE IF NOT EXISTS pdf_import_logs (
    log_id     BIGSERIAL PRIMARY KEY,
    job_id     TEXT NOT NULL REFERENCES pdf_import_jobs(job_id) ON DELETE CASCADE,
    level      TEXT NOT NULL DEFAULT 'info',
    message    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_pdf_import_logs_job ON pdf_import_logs (job_id, log_id);
