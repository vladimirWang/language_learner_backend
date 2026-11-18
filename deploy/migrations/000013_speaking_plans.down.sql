UPDATE level_exams le
SET item_type = 'speaking_topic', exam_id = p.name
FROM speaking_exam_plans p
WHERE le.item_type = 'speaking_plan' AND le.exam_id = p.id::text;

DROP TABLE IF EXISTS speaking_exam_plan_items;
DROP TABLE IF EXISTS speaking_exam_plans;
