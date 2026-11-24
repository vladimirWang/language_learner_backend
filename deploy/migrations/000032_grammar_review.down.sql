-- 回滚语法题审核字段。
DROP INDEX IF EXISTS idx_grammar_questions_review;

ALTER TABLE grammar_questions
    DROP CONSTRAINT IF EXISTS grammar_questions_status_check;

ALTER TABLE grammar_questions
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS generated_by,
    DROP COLUMN IF EXISTS review_note,
    DROP COLUMN IF EXISTS reviewed_at,
    DROP COLUMN IF EXISTS source_sha;
