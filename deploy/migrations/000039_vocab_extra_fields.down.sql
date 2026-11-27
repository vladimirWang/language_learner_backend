ALTER TABLE vocab_words_de
    DROP COLUMN IF EXISTS separable,
    DROP COLUMN IF EXISTS trans_intr,
    DROP COLUMN IF EXISTS core_case,
    DROP COLUMN IF EXISTS sort_key,
    DROP COLUMN IF EXISTS extra_json;

ALTER TABLE vocab_words_en
    DROP COLUMN IF EXISTS trans_intr,
    DROP COLUMN IF EXISTS core_case,
    DROP COLUMN IF EXISTS sort_key,
    DROP COLUMN IF EXISTS extra_json;

ALTER TABLE vocab_words_ja
    DROP COLUMN IF EXISTS sort_key,
    DROP COLUMN IF EXISTS extra_json;
