-- 回滚仅移除索引；被收敛软删除的重复行不可逆（数据清理不恢复）。
DROP INDEX IF EXISTS uq_listening_jobs_exam_id_active;
