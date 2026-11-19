-- listening_job_notifications：持久化系统通知（AI 听力生成、构建完成、PDF 解析等）。
-- 每条记录对应一个事件；前端"消息中心"按 created_at DESC 拉取。
CREATE TABLE IF NOT EXISTS listening_job_notifications (
    id            BIGSERIAL PRIMARY KEY,
    job_id        TEXT NOT NULL,                  -- listening_jobs.id（可为空但一般有）
    event_type    TEXT NOT NULL,                  -- progress / success / error / info
    title         TEXT NOT NULL,
    message       TEXT,
    payload       JSONB,                          -- 可选结构化数据（exam_id / result 摘要等）
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at       TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_ljn_created_at ON listening_job_notifications (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ljn_read_at    ON listening_job_notifications (read_at);
