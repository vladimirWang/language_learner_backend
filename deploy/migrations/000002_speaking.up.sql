CREATE TABLE IF NOT EXISTS speaking_items (
    item_key   TEXT PRIMARY KEY,
    lang       TEXT NOT NULL DEFAULT 'de',
    level      TEXT NOT NULL,               -- A1 / A2 / B1 / B2
    exam_part  TEXT NOT NULL,               -- part1_intro / part2_presentation / part3_discussion
    topic      TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT 'examiner',
    prompt_de  TEXT NOT NULL,               -- 考官德语问句
    prompt_zh  TEXT,                        -- 中文提示
    expected_points TEXT,                   -- 期望覆盖的德语要点，JSON 数组
    ref_answer TEXT,                        -- 参考范例回答
    error_note TEXT,                        -- 常见错误提示
    sort_order INT NOT NULL DEFAULT 0,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_speaking_items_filter
    ON speaking_items (lang, level, active, sort_order);

INSERT INTO speaking_items
    (item_key, lang, level, exam_part, topic, role, prompt_de, prompt_zh, expected_points, ref_answer, error_note, sort_order)
VALUES
    ('de-a1-intro-name', 'de', 'A1', 'part1_intro', 'Person', 'examiner',
     'Wie heißen Sie?', '您叫什么名字？',
     '["heiße", "Name ist"]',
     'Ich heiße Anna. / Mein Name ist Anna.',
     'heißen 是强变化动词：ich heiße, du heißt, er/sie heißt。', 1),
    ('de-a1-intro-herkunft', 'de', 'A1', 'part1_intro', 'Person', 'examiner',
     'Woher kommen Sie?', '您来自哪里？',
     '["komme aus"]',
     'Ich komme aus China.',
     '表示来源用 kommen aus + 国家名，如 aus China, aus Deutschland。', 2),
    ('de-a1-intro-wohnen', 'de', 'A1', 'part1_intro', 'Person', 'examiner',
     'Wo wohnen Sie?', '您住在哪里？',
     '["wohne in"]',
     'Ich wohne in Shanghai.',
     '居住地点用 wohnen in + 城市名。', 3),
    ('de-a1-intro-arbeit', 'de', 'A1', 'part1_intro', 'Arbeit', 'examiner',
     'Was machen Sie von Beruf?', '您的职业是什么？',
     '["arbeite als", "bin"]',
     'Ich arbeite als Ingenieur. / Ich bin Lehrerin.',
     '职业可用 arbeiten als + 职业，或 sein + 职业（不加冠词）。', 4),
    ('de-a1-intro-freizeit', 'de', 'A1', 'part1_intro', 'Freizeit', 'examiner',
     'Was machen Sie in Ihrer Freizeit?', '您空闲时间做什么？',
     '["spiele", "lese", "gehe"]',
     'In meiner Freizeit spiele ich gern Tennis und lese Bücher.',
     '注意动词第二位：In meiner Freizeit spiele ich ...', 5),
    ('de-a1-intro-sprachen', 'de', 'A1', 'part1_intro', 'Person', 'examiner',
     'Welche Sprachen sprechen Sie?', '您会说哪些语言？',
     '["spreche"]',
     'Ich spreche Chinesisch und ein bisschen Deutsch.',
     'sprechen 变位：ich spreche, du sprichst, er/sie spricht。', 6),
    ('de-a2-plan-weekend', 'de', 'A2', 'part2_presentation', 'Freizeit', 'examiner',
     'Was planen Sie für das Wochenende?', '您周末有什么计划？',
     '["werde", "möchte", "vor"]',
     'Am Wochenende werde ich meine Eltern besuchen. Ich möchte auch Sport machen.',
     '将来计划可用 werden + 动词不定式，或 möchten。', 1),
    ('de-a2-reise', 'de', 'A2', 'part2_presentation', 'Reisen', 'examiner',
     'Erzählen Sie von Ihrer letzten Reise.', '请讲讲您最近的一次旅行。',
     '["bin gefahren", "habe besucht", "war"]',
     'Letztes Jahr bin ich nach Berlin gefahren. Ich habe viele Museen besucht.',
     '完成时注意助动词：移动动词多用 sein，其余多用 haben。', 2),
    ('de-a2-essen', 'de', 'A2', 'part2_presentation', 'Essen', 'examiner',
     'Was essen Sie gern?', '您喜欢吃什么？',
     '["esse gern", "schmeckt"]',
     'Ich esse gern Nudeln. Am liebsten schmeckt mir die chinesische Küche.',
     'gern 表示“喜欢”，位置在动词之后：Ich esse gern ...', 3),
    ('de-a2-termin', 'de', 'A2', 'part2_presentation', 'Termin', 'examiner',
     'Wie vereinbaren Sie einen Termin?', '您如何预约一个时间？',
     '["Termin", "können", "Uhr"]',
     'Können wir einen Termin am Montag um zehn Uhr vereinbaren?',
     '约定时间常用 einen Termin vereinbaren；时间表达 am Montag um zehn Uhr。', 4)
ON CONFLICT (item_key) DO NOTHING;
