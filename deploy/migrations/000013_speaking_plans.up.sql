-- 口语方案：同一难度下由若干口语题目组成的可复用方案，关卡配置按方案引用。
CREATE TABLE IF NOT EXISTS speaking_exam_plans (
    id          BIGSERIAL PRIMARY KEY,
    lang        TEXT NOT NULL DEFAULT 'de',
    level       cefr_level NOT NULL,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (lang, level, name)
);

CREATE TABLE IF NOT EXISTS speaking_exam_plan_items (
    plan_id    BIGINT NOT NULL REFERENCES speaking_exam_plans(id) ON DELETE CASCADE,
    item_key   TEXT NOT NULL REFERENCES speaking_items(item_key) ON DELETE CASCADE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (plan_id, item_key)
);

-- 把历史上以「话题」形式加到关卡的口语入口转换为方案：
-- 每个话题生成一个同名方案，收录该难度下该话题的启用题目。
INSERT INTO speaking_exam_plans (lang, level, name, description)
SELECT 'de', l.difficulty::cefr_level, le.exam_id, '由原口语话题自动转换'
FROM level_exams le
JOIN levels l ON l.id = le.level_id
WHERE le.item_type = 'speaking_topic'
GROUP BY l.difficulty, le.exam_id
ON CONFLICT (lang, level, name) DO NOTHING;

INSERT INTO speaking_exam_plan_items (plan_id, item_key)
SELECT p.id, si.item_key
FROM speaking_exam_plans p
JOIN level_exams le ON le.exam_id = p.name AND le.item_type = 'speaking_topic'
JOIN levels l ON l.id = le.level_id AND l.difficulty::cefr_level = p.level
JOIN speaking_items si ON si.level = p.level AND si.topic = p.name AND si.active
GROUP BY p.id, si.item_key
ON CONFLICT DO NOTHING;

UPDATE level_exams le
SET item_type = 'speaking_plan', exam_id = p.id::text
FROM speaking_exam_plans p, levels l
WHERE le.item_type = 'speaking_topic'
  AND l.id = le.level_id
  AND p.level = l.difficulty::cefr_level
  AND p.name = le.exam_id;
