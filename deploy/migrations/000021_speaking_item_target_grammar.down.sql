DROP INDEX IF EXISTS idx_speaking_items_target_grammar;

ALTER TABLE speaking_items
    DROP COLUMN IF EXISTS target_grammar;
