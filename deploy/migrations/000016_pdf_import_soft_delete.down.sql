DROP INDEX IF EXISTS idx_pdf_import_jobs_deleted_at;
ALTER TABLE pdf_import_jobs DROP COLUMN IF EXISTS deleted_at;
