-- listening_jobs.level 由 cefr_level 枚举改为 TEXT：
-- 日语词库使用 JLPT N5–N1，枚举只含 A1–C2，按等级查询时会报
-- invalid input value for enum cefr_level: "N5"。
ALTER TABLE listening_jobs ALTER COLUMN level DROP DEFAULT;
ALTER TABLE listening_jobs ALTER COLUMN level TYPE TEXT USING level::text;
ALTER TABLE listening_jobs ALTER COLUMN level SET DEFAULT 'A1';
