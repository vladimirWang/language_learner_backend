-- 口语题目可自然引出的语法点，用于方案级语法要求的承载校验。
ALTER TABLE speaking_items
    ADD COLUMN IF NOT EXISTS target_grammar JSONB NOT NULL DEFAULT '[]';

CREATE INDEX IF NOT EXISTS idx_speaking_items_target_grammar
    ON speaking_items USING GIN (target_grammar);
