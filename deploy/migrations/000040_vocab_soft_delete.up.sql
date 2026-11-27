-- 词条软删除：管理台误删后可恢复，物理行保留以免学习记录与每日分配指向悬空 id。
-- 所有对外读取（列表、每日新词、关卡词池、最近学过、导出与构库）都只取 deleted_at IS NULL。

ALTER TABLE vocab_words_de ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE vocab_words_en ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE vocab_words_ja ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_vocab_words_de_active
    ON vocab_words_de (level) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vocab_words_en_active
    ON vocab_words_en (level) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vocab_words_ja_active
    ON vocab_words_ja (level) WHERE deleted_at IS NULL;
