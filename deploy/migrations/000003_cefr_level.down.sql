DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'speaking_items'
          AND column_name = 'level'
          AND udt_name = 'cefr_level'
    ) THEN
        ALTER TABLE speaking_items ALTER COLUMN level TYPE TEXT USING level::text;
    END IF;
END $$;

DROP INDEX IF EXISTS idx_listening_jobs_level;
ALTER TABLE listening_jobs DROP COLUMN IF EXISTS level;
DROP TYPE IF EXISTS cefr_level;
