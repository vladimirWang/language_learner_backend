-- 补齐语种词表与 content/<lang>/vocab.csv 之间的字段缺口。
-- 可分动词 / 及物性 / 支配格 / 排序键 / extra_json 原先只存在于 CSV，
-- 库里没有对应列，导致数据库无法作为词库的唯一真值（导出会丢数据），
-- builder 也只能反向用旧 CSV 覆盖库内数据。

ALTER TABLE vocab_words_de
    ADD COLUMN IF NOT EXISTS separable  BOOLEAN,
    ADD COLUMN IF NOT EXISTS trans_intr TEXT,
    ADD COLUMN IF NOT EXISTS core_case  TEXT,
    ADD COLUMN IF NOT EXISTS sort_key   TEXT,
    ADD COLUMN IF NOT EXISTS extra_json JSONB;

ALTER TABLE vocab_words_en
    ADD COLUMN IF NOT EXISTS trans_intr TEXT,
    ADD COLUMN IF NOT EXISTS core_case  TEXT,
    ADD COLUMN IF NOT EXISTS sort_key   TEXT,
    ADD COLUMN IF NOT EXISTS extra_json JSONB;

ALTER TABLE vocab_words_ja
    ADD COLUMN IF NOT EXISTS sort_key   TEXT,
    ADD COLUMN IF NOT EXISTS extra_json JSONB;
