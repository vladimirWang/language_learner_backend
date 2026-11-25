-- 解除每日关卡与日期的绑定：关卡改为同一 (lang, level) 下按 id 升序的有序序列，
-- 解锁由上一关 best_stars>=2 决定，不再按日期控制可见性。
-- 注意：连续打卡 streak 仍保留（user_streaks 表 + Redis bitmap），只是打卡判定
-- 由「完成当天关卡」改为「当天任意已解锁关卡提交且 best_stars>=2」，故本表不动。
--
-- 1) 删除 (lang, level, day) 唯一约束：允许缓冲预生成多关共享同一天。
--    day 列保留为「生成日期」信息字段，不再作为关卡身份。
ALTER TABLE daily_challenges
    DROP CONSTRAINT IF EXISTS daily_challenges_lang_level_day_key;

-- 2) 序列查询索引：按 lang + level 过滤、按 id 升序排列。
CREATE INDEX IF NOT EXISTS idx_daily_challenges_sequence
    ON daily_challenges (lang, level, id);
