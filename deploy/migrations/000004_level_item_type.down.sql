DROP INDEX IF EXISTS uq_level_exams_level_type_exam;

DELETE FROM level_exams WHERE item_type <> 'listening';
ALTER TABLE level_exams DROP COLUMN IF EXISTS item_type;

ALTER TABLE level_exams
    ADD CONSTRAINT level_exams_level_id_exam_id_key UNIQUE (level_id, exam_id);
