-- 管理台按关卡统计参与人数：COUNT(DISTINCT openid) WHERE challenge_id = ?。
CREATE INDEX IF NOT EXISTS idx_challenge_attempts_challenge
    ON challenge_attempts (challenge_id, openid);
