ALTER TABLE speaking_reply_templates DROP COLUMN IF EXISTS topics;
DROP INDEX IF EXISTS idx_speaking_reply_templates_topics;
