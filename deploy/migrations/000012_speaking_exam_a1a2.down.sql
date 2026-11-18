-- 回滚：删除本次新增的 A1/A2 题目，恢复被修改的旧题文案与旧样例题状态。
DELETE FROM speaking_items
WHERE item_key IN (
    'de-a1-intro-alter',
    'de-a1-qa-essen', 'de-a1-qa-einkaufen', 'de-a1-qa-wochenende', 'de-a1-qa-geschwister',
    'de-a1-request-buch', 'de-a1-request-kino', 'de-a1-request-wort',
    'de-a2-intro-geburtstag', 'de-a2-intro-wohnort', 'de-a2-intro-hobby', 'de-a2-intro-beruf',
    'de-a2-mono-geld', 'de-a2-plan-ausflug'
);

UPDATE speaking_items
SET prompt_de = 'Was machen Sie von Beruf?', updated_at = now()
WHERE item_key = 'de-a1-intro-arbeit';

UPDATE speaking_items
SET prompt_de = 'Was machen Sie in Ihrer Freizeit?', updated_at = now()
WHERE item_key = 'de-a1-intro-freizeit';

UPDATE speaking_items
SET active = true, updated_at = now()
WHERE item_key IN (
    'de-a1-presentation-familie', 'de-a1-presentation-tagesablauf',
    'de-a1-discussion-fest', 'de-a1-discussion-wochenende',
    'de-a2-plan-weekend', 'de-a2-reise', 'de-a2-essen', 'de-a2-termin',
    'de-a2-presentation-wohnung', 'de-a2-presentation-reise',
    'de-a2-discussion-geschenk', 'de-a2-discussion-verkehr'
);

DROP TABLE IF EXISTS speaking_exam_parts;
