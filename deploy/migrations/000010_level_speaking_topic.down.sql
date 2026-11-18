-- 回退：把话题关联还原为话题下当前等级的全部单题关联。
INSERT INTO level_exams (level_id, exam_id, item_type, sort_order, created_at)
SELECT le.level_id, si.item_key, 'speaking', le.sort_order, now()
FROM level_exams le
JOIN levels l ON l.id = le.level_id
JOIN speaking_items si ON si.topic = le.exam_id AND si.lang = l.lang AND si.level::text = l.difficulty
WHERE le.item_type = 'speaking_topic'
ON CONFLICT DO NOTHING;

DELETE FROM level_exams WHERE item_type = 'speaking_topic';
