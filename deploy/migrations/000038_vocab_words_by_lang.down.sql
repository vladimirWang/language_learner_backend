-- 回滚：合并回单表。三张表共用同一个 sequence，重建时直接沿用。
CREATE TABLE IF NOT EXISTS vocab_words (
    id             BIGINT PRIMARY KEY DEFAULT nextval('vocab_words_id_seq'),
    lang           TEXT NOT NULL,
    lemma          TEXT NOT NULL,
    level          TEXT NOT NULL,
    word_type      TEXT,
    gender         TEXT,
    plural         TEXT,
    chinese_mean   TEXT NOT NULL,
    english_mean   TEXT,
    example_de     TEXT,
    image_url      TEXT,
    source_md_path TEXT,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (lang, lemma)
);

INSERT INTO vocab_words
    (id, lang, lemma, level, word_type, gender, plural, chinese_mean, english_mean,
     example_de, image_url, source_md_path, updated_at)
SELECT id, 'de', lemma, level, word_type, gender, plural, chinese_mean, english_mean,
       example_de, image_url, source_md_path, updated_at
FROM vocab_words_de;

INSERT INTO vocab_words
    (id, lang, lemma, level, word_type, gender, plural, chinese_mean, english_mean,
     example_de, image_url, source_md_path, updated_at)
SELECT id, 'en', lemma, level, word_type, NULL, NULL, chinese_mean, english_mean,
       example_en, image_url, source_md_path, updated_at
FROM vocab_words_en;

INSERT INTO vocab_words
    (id, lang, lemma, level, word_type, gender, plural, chinese_mean, english_mean,
     example_de, image_url, source_md_path, updated_at)
SELECT id, 'ja', lemma, level, word_type, NULL, NULL, chinese_mean, english_mean,
       example_ja, image_url, source_md_path, updated_at
FROM vocab_words_ja;

DROP TABLE vocab_words_de;
DROP TABLE vocab_words_en;
DROP TABLE vocab_words_ja;

ALTER SEQUENCE vocab_words_id_seq OWNED BY vocab_words.id;

CREATE INDEX IF NOT EXISTS idx_vocab_words_daily ON vocab_words (lang, level, lemma);
