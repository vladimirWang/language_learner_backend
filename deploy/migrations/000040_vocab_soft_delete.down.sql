DROP INDEX IF EXISTS idx_vocab_words_de_active;
DROP INDEX IF EXISTS idx_vocab_words_en_active;
DROP INDEX IF EXISTS idx_vocab_words_ja_active;

ALTER TABLE vocab_words_de DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE vocab_words_en DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE vocab_words_ja DROP COLUMN IF EXISTS deleted_at;
