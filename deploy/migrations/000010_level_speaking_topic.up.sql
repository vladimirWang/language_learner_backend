-- 口语关卡关联从「单题」调整为「话题」：
-- 旧行 item_type='speaking' 的 exam_id 是题目 key，这里按话题聚合后替换。
INSERT INTO level_exams (level_id, exam_id, item_type, sort_order, created_at)
SELECT le.level_id, si.topic, 'speaking_topic', MIN(le.sort_order), now()
FROM level_exams le
JOIN speaking_items si ON si.item_key = le.exam_id
WHERE le.item_type = 'speaking'
GROUP BY le.level_id, si.topic
ON CONFLICT DO NOTHING;

DELETE FROM level_exams WHERE item_type = 'speaking';
