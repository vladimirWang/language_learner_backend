-- 回滚：删除序列索引、恢复 (lang, level, day) 唯一约束。
-- 注意：若 daily_challenges 已存在同日多条记录，恢复唯一约束会失败，需先人工去重。
-- user_streaks 表保留不动（streak 仍在使用）。
DROP INDEX IF EXISTS idx_daily_challenges_sequence;

ALTER TABLE daily_challenges
    ADD CONSTRAINT daily_challenges_lang_level_day_key UNIQUE (lang, level, day);
