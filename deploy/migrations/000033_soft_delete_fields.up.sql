-- 给语法题库、口语题库、口语方案表加软删除字段 deleted_at。
-- listening_jobs 已有该字段，无需改动。

ALTER TABLE grammar_questions ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE speaking_items ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE speaking_exam_plans ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

-- 索引加速列表查询（软删除过滤）
CREATE INDEX IF NOT EXISTS idx_grammar_questions_deleted_at ON grammar_questions (deleted_at) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_speaking_items_deleted_at ON speaking_items (deleted_at) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_speaking_exam_plans_deleted_at ON speaking_exam_plans (deleted_at) WHERE deleted_at IS NULL;
