-- 语法点引导问句，竖线分隔，供语法要求定向补答与内容提示。
ALTER TABLE rag_chunks
    ADD COLUMN IF NOT EXISTS prompts TEXT NOT NULL DEFAULT '';
