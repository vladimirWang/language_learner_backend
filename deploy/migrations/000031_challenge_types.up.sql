-- 每日关卡分型：每关只出一种题型，按 单词/搭配 → 语法 → 听力 轮转。
ALTER TABLE daily_challenges
    ADD COLUMN IF NOT EXISTS challenge_type TEXT NOT NULL DEFAULT 'vocab';

ALTER TABLE daily_challenges
    DROP CONSTRAINT IF EXISTS daily_challenges_type_check;
ALTER TABLE daily_challenges
    ADD CONSTRAINT daily_challenges_type_check
    CHECK (challenge_type IN ('vocab', 'grammar', 'listening'));

-- 听力题进入题目快照。
ALTER TABLE daily_challenge_items
    DROP CONSTRAINT IF EXISTS daily_challenge_items_kind_check;
ALTER TABLE daily_challenge_items
    ADD CONSTRAINT daily_challenge_items_kind_check
    CHECK (kind IN ('word', 'collocation', 'grammar', 'listening'));

-- 结构化语法题库：由素材仓库导入，数量不足时生成器用 rag_chunks 模板兜底。
CREATE TABLE IF NOT EXISTS grammar_questions (
    id          BIGSERIAL PRIMARY KEY,
    lang        TEXT NOT NULL,
    level       TEXT NOT NULL DEFAULT '',
    point_key   TEXT NOT NULL DEFAULT '',
    point_label TEXT NOT NULL DEFAULT '',
    stem        TEXT NOT NULL,
    options     JSONB NOT NULL,
    answer      TEXT NOT NULL,
    explanation TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (lang, stem)
);

CREATE INDEX IF NOT EXISTS idx_grammar_questions_pick
    ON grammar_questions (lang, level, active);
