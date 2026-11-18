-- 统一 CEFR 等级枚举：听力试题与口语试题共用。
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'cefr_level') THEN
        CREATE TYPE cefr_level AS ENUM ('A1', 'A2', 'B1', 'B2', 'C1', 'C2');
    END IF;
END $$;

-- 听力任务增加等级字段，默认 A1。
ALTER TABLE listening_jobs ADD COLUMN IF NOT EXISTS level cefr_level NOT NULL DEFAULT 'A1';
CREATE INDEX IF NOT EXISTS idx_listening_jobs_level ON listening_jobs (level);

-- 口语题目等级由 TEXT 收敛为共享枚举。
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'speaking_items'
          AND column_name = 'level'
          AND data_type = 'text'
    ) THEN
        ALTER TABLE speaking_items
            ALTER COLUMN level TYPE cefr_level USING upper(level)::cefr_level;
    END IF;
END $$;
