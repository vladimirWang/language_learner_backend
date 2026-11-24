-- 语法题审核流：agent 只写草稿，审核通过后才能进入关卡。
ALTER TABLE grammar_questions
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'draft',
    ADD COLUMN IF NOT EXISTS generated_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS review_note TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS reviewed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS source_sha TEXT NOT NULL DEFAULT '';

ALTER TABLE grammar_questions
    DROP CONSTRAINT IF EXISTS grammar_questions_status_check;
ALTER TABLE grammar_questions
    ADD CONSTRAINT grammar_questions_status_check
    CHECK (status IN ('draft', 'approved', 'rejected'));

CREATE INDEX IF NOT EXISTS idx_grammar_questions_review
    ON grammar_questions (status, lang, level);
