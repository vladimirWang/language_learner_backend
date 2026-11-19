-- 每日分配：同一 device 同一天返回同一批词；level='' 表示跨等级抽取。
CREATE TABLE IF NOT EXISTS daily_word_assignments (
    lang       TEXT NOT NULL,
    level      TEXT NOT NULL DEFAULT '',
    day        DATE NOT NULL,
    device_id  TEXT NOT NULL DEFAULT '',
    word_ids   BIGINT[] NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (lang, level, day, device_id)
);

-- 学习记录：决定“最近五天学的新词”。
CREATE TABLE IF NOT EXISTS user_word_learning (
    device_id  TEXT NOT NULL DEFAULT '',
    word_id    BIGINT NOT NULL REFERENCES vocab_words(id) ON DELETE CASCADE,
    learned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, word_id)
);

CREATE INDEX IF NOT EXISTS idx_user_word_learning_recent
    ON user_word_learning (device_id, learned_at DESC);
