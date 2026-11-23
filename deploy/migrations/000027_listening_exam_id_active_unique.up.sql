-- 收敛同一 exam_id 的重复活跃记录：仅保留 updated_at 最新的一条，其余软删除。
-- 历史上 overwrite 上传会为同一 exam_id 累积多条未删除记录，加唯一索引前必须先收敛，
-- 否则 CREATE UNIQUE INDEX 会因现存重复值失败。
WITH ranked AS (
    SELECT job_id,
           ROW_NUMBER() OVER (
               PARTITION BY exam_id
               ORDER BY updated_at DESC, created_at DESC, job_id DESC
           ) AS rn
    FROM listening_jobs
    WHERE deleted_at IS NULL
)
UPDATE listening_jobs j
SET deleted_at = now(),
    updated_at = now()
FROM ranked r
WHERE j.job_id = r.job_id
  AND r.rn > 1;

-- 部分唯一索引：保证同一 exam_id 至多一条活跃（未删除）记录。
-- 软删除的历史记录不受此约束限制，可与新上传的记录共存。
CREATE UNIQUE INDEX IF NOT EXISTS uq_listening_jobs_exam_id_active
    ON listening_jobs (exam_id)
    WHERE deleted_at IS NULL;
