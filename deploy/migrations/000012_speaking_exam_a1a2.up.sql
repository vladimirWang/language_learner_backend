-- A1/A2 口语考试：把原先硬编码在客户端的考试结构（部分时长、追问上限、辅助材料）
-- 与题目统一落库。
--   1. speaking_exam_parts：每场考试的固定部分配置，aid 存 JSONB；
--   2. speaking_items：题目本体，复用现有题库表，管理台可直接维护；
--   3. 迁移后 A1/A2 只使用官方流程题库（旧样例题置为 inactive，保留数据）。
CREATE TABLE IF NOT EXISTS speaking_exam_parts (
    id            BIGSERIAL PRIMARY KEY,
    lang          TEXT NOT NULL DEFAULT 'de',
    level         TEXT NOT NULL,
    part_key      TEXT NOT NULL,
    title         TEXT NOT NULL,
    mode          TEXT NOT NULL,
    duration_sec  INT  NOT NULL DEFAULT 180,
    max_followups INT  NOT NULL DEFAULT 0,
    role          TEXT NOT NULL DEFAULT 'examiner',
    -- 考试辅助材料：chips / cards / mindmap / schedule / topic
    aid           JSONB,
    sort_order    INT  NOT NULL DEFAULT 0,
    active        BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (lang, level, part_key)
);

CREATE INDEX IF NOT EXISTS idx_speaking_exam_parts
    ON speaking_exam_parts (lang, level, active, sort_order);

INSERT INTO speaking_exam_parts
    (lang, level, part_key, title, mode, duration_sec, max_followups, role, aid, sort_order)
VALUES
    ('de', 'A1', 'part1_intro', '自我介绍', 'intro', 150, 0, 'examiner', NULL, 1),
    ('de', 'A1', 'part2_presentation', '日常问答', 'qa', 180, 1, 'examiner',
     $aid${"kind":"cards","label":"抽一张卡，围绕卡片简短问答","cards":[{"title":"Essen","hint":"吃什么、是否自己做饭"},{"title":"Einkaufen","hint":"在哪里买、常买什么"},{"title":"Wochenende","hint":"周末做什么"},{"title":"Familie","hint":"家庭成员、是否有兄弟姐妹"}]}$aid$::jsonb,
     2),
    ('de', 'A1', 'part3_discussion', '请求互动', 'request', 150, 0, 'examiner',
     $aid${"kind":"cards","label":"根据情景提出请求","cards":[{"title":"Buch","hint":"请考官把书递给你"},{"title":"Kino","hint":"约考官周六去看电影"},{"title":"Wort","hint":"没听懂某个词，请对方再说一遍"}]}$aid$::jsonb,
     3),
    ('de', 'A2', 'part1_intro', '个人信息问答', 'qa', 240, 1, 'partner',
     $aid${"kind":"cards","label":"双方轮流抽取关键词卡提问","cards":[{"title":"Geburtstag","hint":"生日在什么时候"},{"title":"Wohnort","hint":"住在哪里、住得怎么样"},{"title":"Hobby","hint":"爱好、多久做一次"},{"title":"Beruf","hint":"职业、工作内容"}]}$aid$::jsonb,
     1),
    ('de', 'A2', 'part2_presentation', '生活简短自述', 'monologue', 180, 1, 'examiner',
     $aid${"kind":"mindmap","label":"围绕思维导图说 3–5 句话，不需要长篇演讲","center":"Geld","branches":["Kleidung","Lebensmittel","Miete","Sparen","Reisen"]}$aid$::jsonb,
     2),
    ('de', 'A2', 'part3_discussion', '共同规划活动', 'planning', 240, 3, 'partner',
     $aid${"kind":"schedule","label":"根据两人的日程，商量时间和地点","groups":[{"name":"Sie","slots":["10:00 Deutschkurs","13:00 Mittagessen","15:00 frei"]},{"name":"Partner","slots":["09:00 Arbeit","12:00 frei","14:00 Termin"]}]}$aid$::jsonb,
     3)
ON CONFLICT (lang, level, part_key) DO UPDATE SET
    title         = EXCLUDED.title,
    mode          = EXCLUDED.mode,
    duration_sec  = EXCLUDED.duration_sec,
    max_followups = EXCLUDED.max_followups,
    role          = EXCLUDED.role,
    aid           = EXCLUDED.aid,
    sort_order    = EXCLUDED.sort_order,
    active        = true,
    updated_at    = now();

-- A1 题目
INSERT INTO speaking_items
    (item_key, lang, level, exam_part, topic, role, prompt_de, prompt_zh,
     expected_points, ref_answer, error_note, sort_order, active)
VALUES
    ('de-a1-intro-name', 'de', 'A1', 'part1_intro', 'Person', 'examiner',
     'Wie heißen Sie?', '你叫什么名字？', '["heiße", "Name ist"]',
     'Ich heiße Anna. / Mein Name ist Anna.', 'heißen 是强变化动词：ich heiße, du heißt, er/sie heißt。', 1, true),
    ('de-a1-intro-alter', 'de', 'A1', 'part1_intro', 'Person', 'examiner',
     'Wie alt sind Sie?', '你多大了？', '[]', '', '', 2, true),
    ('de-a1-intro-herkunft', 'de', 'A1', 'part1_intro', 'Person', 'examiner',
     'Woher kommen Sie?', '你来自哪里？', '["komme aus"]',
     'Ich komme aus China.', '表示来源用 kommen aus + 国家名，如 aus China, aus Deutschland。', 3, true),
    ('de-a1-intro-wohnen', 'de', 'A1', 'part1_intro', 'Person', 'examiner',
     'Wo wohnen Sie?', '你住在哪里？', '["wohne in"]',
     'Ich wohne in Shanghai.', '居住地点用 wohnen in + 城市名。', 4, true),
    ('de-a1-intro-sprachen', 'de', 'A1', 'part1_intro', 'Person', 'examiner',
     'Welche Sprachen sprechen Sie?', '你说哪些语言？', '["spreche"]',
     'Ich spreche Chinesisch und ein bisschen Deutsch.',
     'sprechen 变位：ich spreche, du sprichst, er/sie spricht。', 5, true),
    ('de-a1-intro-arbeit', 'de', 'A1', 'part1_intro', 'Person', 'examiner',
     'Was sind Sie von Beruf?', '你的职业是什么？', '["arbeite als", "bin"]',
     'Ich arbeite als Ingenieur. / Ich bin Lehrerin.',
     '职业可用 arbeiten als + 职业，或 sein + 职业（不加冠词）。', 6, true),
    ('de-a1-intro-freizeit', 'de', 'A1', 'part1_intro', 'Freizeit', 'examiner',
     'Was machen Sie gern in Ihrer Freizeit?', '你空闲时喜欢做什么？', '["spiele", "lese", "gehe"]',
     'In meiner Freizeit spiele ich gern Tennis und lese Bücher.',
     '注意动词第二位：In meiner Freizeit spiele ich ...', 7, true),

    ('de-a1-qa-essen', 'de', 'A1', 'part2_presentation', 'Essen', 'examiner',
     'Was essen Sie gern?', '你喜欢吃什么？', '[]', '', '', 1, true),
    ('de-a1-qa-einkaufen', 'de', 'A1', 'part2_presentation', 'Einkaufen', 'examiner',
     'Wo kaufen Sie ein?', '你在哪里购物？', '[]', '', '', 2, true),
    ('de-a1-qa-wochenende', 'de', 'A1', 'part2_presentation', 'Freizeit', 'examiner',
     'Was machen Sie am Wochenende?', '你周末做什么？', '[]', '', '', 3, true),
    ('de-a1-qa-geschwister', 'de', 'A1', 'part2_presentation', 'Familie', 'examiner',
     'Haben Sie Geschwister?', '你有兄弟姐妹吗？', '[]', '', '', 4, true),

    ('de-a1-request-buch', 'de', 'A1', 'part3_discussion', 'Bitten', 'examiner',
     'Sie möchten mein Buch haben. Bitten Sie mich!', '你想要我的书，请礼貌地提出请求。', '[]', '', '', 1, true),
    ('de-a1-request-kino', 'de', 'A1', 'part3_discussion', 'Bitten', 'examiner',
     'Sie möchten am Samstag ins Kino gehen. Fragen Sie mich!', '你想周六去看电影，请问问考官。', '[]', '', '', 2, true),
    ('de-a1-request-wort', 'de', 'A1', 'part3_discussion', 'Bitten', 'examiner',
     'Sie verstehen ein Wort nicht. Fragen Sie mich!', '你没听懂一个词，请对方再说一遍。', '[]', '', '', 3, true),

-- A2 题目
    ('de-a2-intro-geburtstag', 'de', 'A2', 'part1_intro', 'Person', 'partner',
     'Wann haben Sie Geburtstag?', '你的生日是什么时候？', '[]', '', '', 1, true),
    ('de-a2-intro-wohnort', 'de', 'A2', 'part1_intro', 'Wohnen', 'partner',
     'Wo wohnen Sie, und wie gefällt es Ihnen dort?', '你住在哪里？那里怎么样？', '[]', '', '', 2, true),
    ('de-a2-intro-hobby', 'de', 'A2', 'part1_intro', 'Freizeit', 'partner',
     'Was ist Ihr Hobby, und wie oft machen Sie das?', '你的爱好是什么？多久做一次？', '[]', '', '', 3, true),
    ('de-a2-intro-beruf', 'de', 'A2', 'part1_intro', 'Beruf', 'partner',
     'Was machen Sie beruflich?', '你做什么工作？', '[]', '', '', 4, true),

    ('de-a2-mono-geld', 'de', 'A2', 'part2_presentation', 'Geld', 'examiner',
     'Erzählen Sie bitte kurz: Was machen Sie mit Ihrem Geld?', '请简短说说：你怎么花你的钱？', '[]', '', '', 1, true),

    ('de-a2-plan-ausflug', 'de', 'A2', 'part3_discussion', 'Planen', 'partner',
     'Wir möchten am Samstag einen Ausflug machen. Haben Sie Zeit?', '我们周六想出去郊游，你有时间吗？', '[]', '', '', 1, true)
ON CONFLICT (item_key) DO UPDATE SET
    level      = EXCLUDED.level,
    exam_part  = EXCLUDED.exam_part,
    topic      = EXCLUDED.topic,
    role       = EXCLUDED.role,
    prompt_de  = EXCLUDED.prompt_de,
    prompt_zh  = EXCLUDED.prompt_zh,
    sort_order = EXCLUDED.sort_order,
    active     = true,
    updated_at = now();

-- A1/A2 官方流程只使用上面这套题：旧样例题保留数据但不再进入考试。
UPDATE speaking_items
SET active = false, updated_at = now()
WHERE level IN ('A1', 'A2')
  AND item_key NOT IN (
    'de-a1-intro-name', 'de-a1-intro-alter', 'de-a1-intro-herkunft', 'de-a1-intro-wohnen',
    'de-a1-intro-sprachen', 'de-a1-intro-arbeit', 'de-a1-intro-freizeit',
    'de-a1-qa-essen', 'de-a1-qa-einkaufen', 'de-a1-qa-wochenende', 'de-a1-qa-geschwister',
    'de-a1-request-buch', 'de-a1-request-kino', 'de-a1-request-wort',
    'de-a2-intro-geburtstag', 'de-a2-intro-wohnort', 'de-a2-intro-hobby', 'de-a2-intro-beruf',
    'de-a2-mono-geld', 'de-a2-plan-ausflug'
  );
