-- wx_users：微信小程序用户。openid 是微信用户在单个小程序内的唯一标识。
-- 用 code2session 拿到的 openid 作为天然主键；unionid 可选（需开放平台绑定）。
CREATE TABLE IF NOT EXISTS wx_users (
    id          BIGSERIAL PRIMARY KEY,
    openid      TEXT NOT NULL UNIQUE,
    unionid     TEXT,
    nickname    TEXT NOT NULL DEFAULT '',
    avatar_url  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_wx_users_unionid ON wx_users (unionid);
