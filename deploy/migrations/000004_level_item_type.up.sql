-- 关卡关联支持听力与口语两类题目。
ALTER TABLE level_exams ADD COLUMN IF NOT EXISTS item_type TEXT NOT NULL DEFAULT 'listening';

-- 原唯一约束只按 exam_id 去重，改为 (level_id, item_type, exam_id)。
ALTER TABLE level_exams DROP CONSTRAINT IF EXISTS level_exams_level_id_exam_id_key;
CREATE UNIQUE INDEX IF NOT EXISTS uq_level_exams_level_type_exam
    ON level_exams (level_id, item_type, exam_id);
