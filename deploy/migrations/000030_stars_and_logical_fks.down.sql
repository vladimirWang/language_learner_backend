-- 回滚时恢复历史物理外键，再删除星星字段。
ALTER TABLE test_items
    ADD CONSTRAINT level_exams_level_id_fkey FOREIGN KEY (level_id) REFERENCES tests(id) ON DELETE CASCADE;
ALTER TABLE speaking_exam_plan_items
    ADD CONSTRAINT speaking_exam_plan_items_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES speaking_exam_plans(id) ON DELETE CASCADE,
    ADD CONSTRAINT speaking_exam_plan_items_item_key_fkey FOREIGN KEY (item_key) REFERENCES speaking_items(item_key) ON DELETE CASCADE;
ALTER TABLE pdf_import_logs
    ADD CONSTRAINT pdf_import_logs_job_id_fkey FOREIGN KEY (job_id) REFERENCES pdf_import_jobs(job_id) ON DELETE CASCADE;
ALTER TABLE pdf_upload_chunks
    ADD CONSTRAINT pdf_upload_chunks_upload_id_fkey FOREIGN KEY (upload_id) REFERENCES pdf_upload_sessions(upload_id) ON DELETE CASCADE;
ALTER TABLE user_word_learning
    ADD CONSTRAINT user_word_learning_word_id_fkey FOREIGN KEY (word_id) REFERENCES vocab_words(id) ON DELETE CASCADE;
ALTER TABLE daily_challenge_items
    ADD CONSTRAINT daily_challenge_items_challenge_id_fkey FOREIGN KEY (challenge_id) REFERENCES daily_challenges(id) ON DELETE CASCADE;
ALTER TABLE challenge_attempts
    ADD CONSTRAINT challenge_attempts_challenge_id_fkey FOREIGN KEY (challenge_id) REFERENCES daily_challenges(id) ON DELETE CASCADE;

ALTER TABLE challenge_attempts
    DROP CONSTRAINT challenge_attempts_stars_check,
    DROP CONSTRAINT challenge_attempts_best_stars_check,
    DROP COLUMN best_stars,
    DROP COLUMN stars;

ALTER TABLE wx_users
    DROP CONSTRAINT wx_users_total_stars_check,
    DROP COLUMN total_stars;
