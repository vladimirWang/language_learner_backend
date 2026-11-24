-- 星星、累计星星与逻辑外键：保留最近作答及每关历史最高星。
ALTER TABLE challenge_attempts
    ADD COLUMN stars SMALLINT NOT NULL DEFAULT 0,
    ADD COLUMN best_stars SMALLINT NOT NULL DEFAULT 0,
    ADD CONSTRAINT challenge_attempts_stars_check CHECK (stars BETWEEN 0 AND 3),
    ADD CONSTRAINT challenge_attempts_best_stars_check CHECK (best_stars BETWEEN 0 AND 3);

ALTER TABLE wx_users
    ADD COLUMN total_stars BIGINT NOT NULL DEFAULT 0,
    ADD CONSTRAINT wx_users_total_stars_check CHECK (total_stars >= 0);

-- 6 题每 2 题 1 星；不足 6 题按正确率折算，并以 floor(60%) 答对数兜底至少 1 星。
UPDATE challenge_attempts
SET stars = CASE
        WHEN total <= 0 OR score <= 0 THEN 0
        WHEN total >= 6 THEN LEAST(3, score / 2)
        WHEN score >= total * 3 / 5 AND score * 3 / total < 1 THEN 1
        ELSE LEAST(3, score * 3 / total)
    END;

UPDATE challenge_attempts
SET best_stars = stars,
    passed = stars >= 2;

UPDATE wx_users u
SET total_stars = COALESCE((
    SELECT SUM(a.best_stars)
    FROM challenge_attempts a
    WHERE a.openid = u.openid
), 0);

-- 全库关联统一改为逻辑外键，由 Go 事务校验父记录并维护删除行为。
ALTER TABLE test_items DROP CONSTRAINT IF EXISTS level_exams_level_id_fkey;
ALTER TABLE test_items DROP CONSTRAINT IF EXISTS test_items_level_id_fkey;
ALTER TABLE speaking_exam_plan_items DROP CONSTRAINT IF EXISTS speaking_exam_plan_items_plan_id_fkey;
ALTER TABLE speaking_exam_plan_items DROP CONSTRAINT IF EXISTS speaking_exam_plan_items_item_key_fkey;
ALTER TABLE pdf_import_logs DROP CONSTRAINT IF EXISTS pdf_import_logs_job_id_fkey;
ALTER TABLE pdf_upload_chunks DROP CONSTRAINT IF EXISTS pdf_upload_chunks_upload_id_fkey;
ALTER TABLE user_word_learning DROP CONSTRAINT IF EXISTS user_word_learning_word_id_fkey;
ALTER TABLE daily_challenge_items DROP CONSTRAINT IF EXISTS daily_challenge_items_challenge_id_fkey;
ALTER TABLE challenge_attempts DROP CONSTRAINT IF EXISTS challenge_attempts_challenge_id_fkey;
