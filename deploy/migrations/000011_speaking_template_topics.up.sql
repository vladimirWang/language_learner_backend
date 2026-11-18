-- 口语话术支持绑定多个话题：topics 为 JSON 字符串数组，空数组表示通用话术。
ALTER TABLE speaking_reply_templates
    ADD COLUMN IF NOT EXISTS topics TEXT NOT NULL DEFAULT '[]';

CREATE INDEX IF NOT EXISTS idx_speaking_reply_templates_topics
    ON speaking_reply_templates (active);
