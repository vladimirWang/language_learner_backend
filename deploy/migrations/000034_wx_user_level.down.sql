ALTER TABLE wx_users
    DROP CONSTRAINT IF EXISTS wx_users_level_check;
ALTER TABLE wx_users
    DROP COLUMN IF EXISTS level;
