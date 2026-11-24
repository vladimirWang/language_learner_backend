-- 回滚关卡分型与语法题库。
DROP TABLE IF EXISTS grammar_questions;

ALTER TABLE daily_challenge_items
    DROP CONSTRAINT IF EXISTS daily_challenge_items_kind_check;
ALTER TABLE daily_challenge_items
    ADD CONSTRAINT daily_challenge_items_kind_check
    CHECK (kind IN ('word', 'collocation', 'grammar'));

ALTER TABLE daily_challenges
    DROP CONSTRAINT IF EXISTS daily_challenges_type_check;
ALTER TABLE daily_challenges
    DROP COLUMN IF EXISTS challenge_type;
