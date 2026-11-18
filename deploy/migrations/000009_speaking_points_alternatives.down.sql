UPDATE speaking_items SET expected_points = '["heiße", "Name ist"]'
WHERE item_key = 'de-a1-intro-name';

UPDATE speaking_items SET expected_points = '["arbeite als", "bin"]'
WHERE item_key = 'de-a1-intro-arbeit';

UPDATE speaking_items SET expected_points = '["spiele", "lese", "gehe"]'
WHERE item_key = 'de-a1-intro-freizeit';

UPDATE speaking_items SET expected_points = '["esse gern", "schmeckt"]'
WHERE item_key = 'de-a2-essen';

UPDATE speaking_items SET expected_points = '["bin gefahren", "habe besucht", "war"]'
WHERE item_key = 'de-a2-reise';
