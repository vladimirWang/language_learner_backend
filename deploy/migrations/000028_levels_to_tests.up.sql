-- 「关卡」模块改名为「测试」：只重命名表，字段、索引与约束名保持不变，
-- 避免影响已有外键与查询。应用层同步改用 tests / test_items。
ALTER TABLE IF EXISTS level_exams RENAME TO test_items;
ALTER TABLE IF EXISTS levels RENAME TO tests;
