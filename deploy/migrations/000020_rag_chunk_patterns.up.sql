-- 语法切片的匹配规则：逗号分隔的正则，用于口语语法要求判定。
ALTER TABLE rag_chunks
    ADD COLUMN IF NOT EXISTS patterns TEXT NOT NULL DEFAULT '';
