-- 方案级语法要求：整场考试任意回答达成即可，不绑定题号/部分。
ALTER TABLE speaking_exam_plans
    ADD COLUMN IF NOT EXISTS grammar_requirements JSONB NOT NULL DEFAULT '[]';
