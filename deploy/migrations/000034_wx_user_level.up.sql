-- 微信用户当前语言等级；当前小程序仅学习德语，等级由服务端作为每日关卡依据。
ALTER TABLE wx_users
    ADD COLUMN IF NOT EXISTS level TEXT NOT NULL DEFAULT 'A1';

ALTER TABLE wx_users
    DROP CONSTRAINT IF EXISTS wx_users_level_check;
ALTER TABLE wx_users
    ADD CONSTRAINT wx_users_level_check
    CHECK (level IN ('A1', 'A2', 'B1', 'B2', 'C1', 'C2', 'N1', 'N2', 'N3', 'N4', 'N5'));
