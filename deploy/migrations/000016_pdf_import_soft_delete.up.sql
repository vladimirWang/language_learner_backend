-- PDF 导入任务支持软删除与恢复：deleted_at 为空表示未删除。
ALTER TABLE pdf_import_jobs ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_pdf_import_jobs_deleted_at ON pdf_import_jobs (deleted_at);
