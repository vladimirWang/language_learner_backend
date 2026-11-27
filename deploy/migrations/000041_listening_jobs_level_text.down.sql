ALTER TABLE listening_jobs ALTER COLUMN level DROP DEFAULT;
ALTER TABLE listening_jobs ALTER COLUMN level TYPE cefr_level USING level::cefr_level;
ALTER TABLE listening_jobs ALTER COLUMN level SET DEFAULT 'A1'::cefr_level;
