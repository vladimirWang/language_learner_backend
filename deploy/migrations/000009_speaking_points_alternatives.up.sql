-- 同一要点内的同义表达用 `|` 分隔，命中任意一个即算覆盖。
-- 适用于单问句题目：考生用一种说法回答正确即可，不必复述所有同义句式。
UPDATE speaking_items SET expected_points = '["heiße|Name ist"]'
WHERE item_key = 'de-a1-intro-name';

UPDATE speaking_items SET expected_points = '["arbeite als|bin"]'
WHERE item_key = 'de-a1-intro-arbeit';

UPDATE speaking_items SET expected_points = '["spiele|lese|gehe"]'
WHERE item_key = 'de-a1-intro-freizeit';

UPDATE speaking_items SET expected_points = '["esse gern|schmeckt"]'
WHERE item_key = 'de-a2-essen';

UPDATE speaking_items SET expected_points = '["bin gefahren|habe besucht|war"]'
WHERE item_key = 'de-a2-reise';
