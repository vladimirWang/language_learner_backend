-- 每日关卡：全局统一关卡（同 lang + level + day 的所有用户同一关）。
-- 内容由 Go 以「真值表 + 模板」生成并落快照；答案来自 vocab_words / collocations / rag_chunks。

-- 固定搭配真值表：首期只有德语有种子数据，其他语种可后续录入。
CREATE TABLE IF NOT EXISTS collocations (
    id          BIGSERIAL PRIMARY KEY,
    lang        TEXT NOT NULL,
    level       TEXT NOT NULL DEFAULT '',
    phrase      TEXT NOT NULL,
    meaning_zh  TEXT NOT NULL DEFAULT '',
    example     TEXT NOT NULL DEFAULT '',
    sort_order  INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (lang, phrase)
);

CREATE INDEX IF NOT EXISTS idx_collocations_pick
    ON collocations (lang, level);

-- 每日关卡主表。status: draft 草稿 / published 已发布。
CREATE TABLE IF NOT EXISTS daily_challenges (
    id          BIGSERIAL PRIMARY KEY,
    lang        TEXT NOT NULL,
    level       TEXT NOT NULL,
    day         DATE NOT NULL,
    title       TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (lang, level, day)
);

CREATE INDEX IF NOT EXISTS idx_daily_challenges_month
    ON daily_challenges (lang, level, day);

-- 关卡题目快照。prompt / answer 为 JSONB；answer 存标准答案，不下发给客户端。
CREATE TABLE IF NOT EXISTS daily_challenge_items (
    id            BIGSERIAL PRIMARY KEY,
    challenge_id  BIGINT NOT NULL REFERENCES daily_challenges(id) ON DELETE CASCADE,
    seq           INT NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('word', 'collocation', 'grammar')),
    ref_id        TEXT NOT NULL DEFAULT '',
    prompt        JSONB NOT NULL,
    answer        JSONB NOT NULL,
    explanation   TEXT NOT NULL DEFAULT '',
    UNIQUE (challenge_id, seq)
);

-- 用户提交记录：同一用户同一关卡只记一次，重复提交返回首次结果。
CREATE TABLE IF NOT EXISTS challenge_attempts (
    id            BIGSERIAL PRIMARY KEY,
    openid        TEXT NOT NULL,
    challenge_id  BIGINT NOT NULL REFERENCES daily_challenges(id) ON DELETE CASCADE,
    answers       JSONB NOT NULL,
    score         INT NOT NULL DEFAULT 0,
    total         INT NOT NULL DEFAULT 0,
    passed        BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (openid, challenge_id)
);

CREATE INDEX IF NOT EXISTS idx_challenge_attempts_openid
    ON challenge_attempts (openid, created_at DESC);

-- 连续打卡天数，按用户 + 语种维度。
CREATE TABLE IF NOT EXISTS user_streaks (
    openid      TEXT NOT NULL,
    lang        TEXT NOT NULL,
    current     INT NOT NULL DEFAULT 0,
    longest     INT NOT NULL DEFAULT 0,
    last_day    DATE,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (openid, lang)
);

-- 德语固定搭配种子（来源：content/de/grammar/高频固定搭配.md）。
INSERT INTO collocations (lang, level, phrase, meaning_zh, example, sort_order) VALUES
    ('de', 'A2', 'warten auf +A',      '等待……',            'Ich warte auf den Bus.', 10),
    ('de', 'A2', 'denken an +A',       '想到……',            'Ich denke an dich.', 20),
    ('de', 'A2', 'träumen von +D',     '梦见 / 梦想……',     'Er träumt von einer Reise.', 30),
    ('de', 'A2', 'sich freuen auf +A', '期待……',            'Wir freuen uns auf den Urlaub.', 40),
    ('de', 'A2', 'sich freuen über +A','为……感到高兴',      'Sie freut sich über das Geschenk.', 50),
    ('de', 'A2', 'Angst haben vor +D', '害怕……',            'Er hat Angst vor dem Hund.', 60),
    ('de', 'A2', 'teilnehmen an +D',   '参加……',            'Er nimmt an dem Kurs teil.', 70),
    ('de', 'B1', 'sich erinnern an +A','回忆起……',          'Ich erinnere mich an den Tag.', 80),
    ('de', 'B1', 'sich vorbereiten auf +A', '为……做准备',   'Wir bereiten uns auf die Prüfung vor.', 90),
    ('de', 'B1', 'Interesse haben an +D', '对……感兴趣',     'Sie hat Interesse an der Geschichte.', 100),
    ('de', 'A2', 'wichtig für +A',     '对……重要',          'Sport ist wichtig für die Gesundheit.', 110),
    ('de', 'A2', 'gut in +D',          '擅长……',            'Er ist gut in Mathematik.', 120),
    ('de', 'A2', 'zufrieden mit +D',   '对……满意',          'Ich bin zufrieden mit dem Ergebnis.', 130),
    ('de', 'B1', 'interessiert an +D', '对……感兴趣',        'Sie ist an Musik interessiert.', 140),
    ('de', 'A2', 'bereit für +A',      '准备好……',          'Wir sind bereit für den Test.', 150),
    ('de', 'B1', 'fertig mit +D',      '做完……',            'Bist du fertig mit der Arbeit?', 160),
    ('de', 'A2', 'Lust haben auf +A',  '想要……',            'Ich habe Lust auf einen Spaziergang.', 170),
    ('de', 'A2', 'Zeit haben für +A',  '有时间做……',        'Ich habe heute keine Zeit für Sport.', 180),
    ('de', 'B1', 'Hunger haben nach +D','想吃……',           'Nach dem Laufen habe ich Hunger nach etwas Süßem.', 190),
    ('de', 'B1', 'Durst haben nach +D','想喝……',            'Er hat Durst nach kaltem Wasser.', 200)
ON CONFLICT (lang, phrase) DO NOTHING;
