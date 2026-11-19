-- 结构化词表：由 builder 按 (lang, lemma) upsert，word_id 跨构建保持稳定。
CREATE TABLE IF NOT EXISTS vocab_words (
    id             BIGSERIAL PRIMARY KEY,
    lang           TEXT NOT NULL,
    lemma          TEXT NOT NULL,
    level          TEXT NOT NULL,
    word_type      TEXT,
    gender         TEXT,
    plural         TEXT,
    chinese_mean   TEXT NOT NULL,
    english_mean   TEXT,
    example_de     TEXT,
    source_md_path TEXT,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (lang, lemma)
);

CREATE INDEX IF NOT EXISTS idx_vocab_words_daily
    ON vocab_words (lang, level, lemma);
