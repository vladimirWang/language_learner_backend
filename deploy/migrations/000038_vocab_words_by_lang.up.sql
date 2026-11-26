-- 单词表按语种拆分：德语需要词性性别与复数，英语需要音标，日语需要假名/汉字/罗马字，
-- 各语种字段差异较大，拆成独立表后列语义更清晰，避免大量恒为空的通用列。
-- 三张表共用 vocab_words_id_seq，word_id 保持全局唯一，user_word_learning 无需改动。

-- 先解除旧表的 sequence 归属，DROP TABLE 时才不会连带删除。
ALTER SEQUENCE IF EXISTS vocab_words_id_seq OWNED BY NONE;

CREATE TABLE IF NOT EXISTS vocab_words_de (
    id             BIGINT PRIMARY KEY DEFAULT nextval('vocab_words_id_seq'),
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
    UNIQUE (lemma)
);

CREATE TABLE IF NOT EXISTS vocab_words_en (
    id             BIGINT PRIMARY KEY DEFAULT nextval('vocab_words_id_seq'),
    lemma          TEXT NOT NULL,
    level          TEXT NOT NULL,
    word_type      TEXT,
    phonetic       TEXT,
    chinese_mean   TEXT NOT NULL,
    english_mean   TEXT,
    example_en     TEXT,
    image_url      TEXT,
    source_md_path TEXT,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (lemma)
);

CREATE TABLE IF NOT EXISTS vocab_words_ja (
    id             BIGINT PRIMARY KEY DEFAULT nextval('vocab_words_id_seq'),
    lemma          TEXT NOT NULL,
    level          TEXT NOT NULL,
    word_type      TEXT,
    kana           TEXT,
    kanji          TEXT,
    romaji         TEXT,
    chinese_mean   TEXT NOT NULL,
    english_mean   TEXT,
    example_ja     TEXT,
    image_url      TEXT,
    source_md_path TEXT,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (lemma)
);

-- 原样搬运旧数据并保留 word_id；旧表的例句列按语种落到各自的 example_* 列。
INSERT INTO vocab_words_de
    (id, lemma, level, word_type, gender, plural, chinese_mean, english_mean,
     example_de, image_url, source_md_path, updated_at)
SELECT id, lemma, level, word_type, gender, plural, chinese_mean, english_mean,
       example_de, image_url, source_md_path, updated_at
FROM vocab_words
WHERE lang = 'de';

INSERT INTO vocab_words_en
    (id, lemma, level, word_type, phonetic, chinese_mean, english_mean,
     example_en, image_url, source_md_path, updated_at)
SELECT id, lemma, level, word_type, NULL, chinese_mean, english_mean,
       example_de, image_url, source_md_path, updated_at
FROM vocab_words
WHERE lang = 'en';

INSERT INTO vocab_words_ja
    (id, lemma, level, word_type, kana, kanji, romaji, chinese_mean, english_mean,
     example_ja, image_url, source_md_path, updated_at)
SELECT id, lemma, level, word_type, NULL, NULL, NULL, chinese_mean, english_mean,
       example_de, image_url, source_md_path, updated_at
FROM vocab_words
WHERE lang = 'ja';
-- 其余语种不在应用支持范围内，迁移时丢弃。

SELECT setval('vocab_words_id_seq', COALESCE((SELECT max(id) FROM vocab_words), 0) + 1, false);

DROP TABLE vocab_words;

CREATE INDEX IF NOT EXISTS idx_vocab_words_de_level ON vocab_words_de (level);
CREATE INDEX IF NOT EXISTS idx_vocab_words_en_level ON vocab_words_en (level);
CREATE INDEX IF NOT EXISTS idx_vocab_words_ja_level ON vocab_words_ja (level);
