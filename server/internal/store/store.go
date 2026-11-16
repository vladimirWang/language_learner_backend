// Package store 封装构建期 PostgreSQL 访问，仅用于版本清单与构建日志。
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Version 对应 build_versions 一行；URL 字段为对象存储 key（本地模式下为文件路径）。
type Version struct {
	Version      string    `json:"version"`
	Lang         string    `json:"lang"`
	DBURL        string    `json:"db_url"`
	VectorURL    string    `json:"vector_snapshot_url"`
	DBMD5        string    `json:"db_md5"`
	VectorMD5    string    `json:"vector_md5"`
	Status       string    `json:"status"`
	SourceCommit string    `json:"source_commit"`
	CreatedAt    time.Time `json:"created_at"`
}

// ListeningJob 对应 listening_jobs 一行。
type ListeningJob struct {
	JobID        string    `json:"job_id"`
	ExamID       string    `json:"exam_id"`
	Lang         string    `json:"lang"`
	SourceURL    string    `json:"source_url"`
	Status       string    `json:"status"`
	ManifestPath string    `json:"manifest_path"`
	LogPath      string    `json:"log_path"`
	ErrorMsg     string    `json:"error_msg"`
	LastLogAt    time.Time `json:"last_log_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ListeningJobLog 对应 listening_job_logs 一行。
type ListeningJobLog struct {
	Stage     string    `json:"stage"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateListeningJob 登记一次听力导入任务。
func (s *Store) CreateListeningJob(ctx context.Context, jobID, examID, lang, sourceURL string) error {
	const q = `
INSERT INTO listening_jobs (job_id, exam_id, lang, source_url, status, updated_at)
VALUES ($1, $2, $3, $4, 'pending', now())
ON CONFLICT (job_id) DO UPDATE SET
  exam_id = EXCLUDED.exam_id,
  lang = EXCLUDED.lang,
  source_url = EXCLUDED.source_url,
  status = 'pending',
  error_msg = NULL,
  manifest_path = NULL,
  log_path = NULL,
  updated_at = now()`
	_, err := s.pool.Exec(ctx, q, jobID, examID, lang, sourceURL)
	return err
}

// StartListeningJob 把状态置为 running。
func (s *Store) StartListeningJob(ctx context.Context, jobID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE listening_jobs SET status='running', updated_at=now() WHERE job_id=$1`, jobID)
	return err
}

// FinishListeningJob 写入成功结果（manifest_path）。
func (s *Store) FinishListeningJob(ctx context.Context, jobID, manifestPath string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE listening_jobs SET status='built', manifest_path=$1, updated_at=now() WHERE job_id=$2`,
		manifestPath, jobID)
	return err
}

// FailListeningJob 写入失败原因。
func (s *Store) FailListeningJob(ctx context.Context, jobID, errorMsg string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE listening_jobs SET status='failed', error_msg=$1, updated_at=now() WHERE job_id=$2`,
		errorMsg, jobID)
	return err
}

// AppendListeningLog 追加一条任务日志，同时刷新 listening_jobs.last_log_at。
func (s *Store) AppendListeningLog(ctx context.Context, jobID, stage, level, message string) error {
	const q = `INSERT INTO listening_job_logs (job_id, stage, level, message) VALUES ($1,$2,$3,$4);
UPDATE listening_jobs SET last_log_at = now() WHERE job_id=$1`
	_, err := s.pool.Exec(ctx, q, jobID, stage, level, message)
	return err
}

// ListListeningJobs 返回最近 N 条任务。
func (s *Store) ListListeningJobs(ctx context.Context, limit int) ([]ListeningJob, error) {
	if limit <= 0 {
		limit = 50
	}
	const q = `
SELECT job_id, exam_id, lang, source_url, status,
       COALESCE(manifest_path,''), COALESCE(log_path,''), COALESCE(error_msg,''),
       last_log_at,
       created_at, updated_at
FROM listening_jobs ORDER BY updated_at DESC LIMIT $1`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []ListeningJob
	for rows.Next() {
		var j ListeningJob
		if err := rows.Scan(&j.JobID, &j.ExamID, &j.Lang, &j.SourceURL, &j.Status,
			&j.ManifestPath, &j.LogPath, &j.ErrorMsg,
			&j.LastLogAt,
			&j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ListeningJobDetail 返回单个任务及其最近日志。
func (s *Store) ListeningJobDetail(ctx context.Context, jobID string) (*ListeningJob, []ListeningJobLog, error) {
	const vq = `
SELECT job_id, exam_id, lang, source_url, status,
       COALESCE(manifest_path,''), COALESCE(log_path,''), COALESCE(error_msg,''),
       last_log_at,
       created_at, updated_at
FROM listening_jobs WHERE job_id=$1`
	var j ListeningJob
	err := s.pool.QueryRow(ctx, vq, jobID).Scan(&j.JobID, &j.ExamID, &j.Lang, &j.SourceURL, &j.Status,
		&j.ManifestPath, &j.LogPath, &j.ErrorMsg,
		&j.LastLogAt,
		&j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	const lq = `
SELECT COALESCE(stage,''), COALESCE(level,''), message, created_at
FROM listening_job_logs WHERE job_id=$1 ORDER BY log_id DESC LIMIT 300`
	rows, err := s.pool.Query(ctx, lq, jobID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var logs []ListeningJobLog
	for rows.Next() {
		var l ListeningJobLog
		if err := rows.Scan(&l.Stage, &l.Level, &l.Message, &l.CreatedAt); err != nil {
			return nil, nil, err
		}
		logs = append(logs, l)
	}
	return &j, logs, rows.Err()
}

// LogEntry 对应 build_logs 一行。
type LogEntry struct {
	Stage     string    `json:"stage"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// Store 持有 pgx 连接池；创建时不建立连接，PG 不可用不影响服务启动。
type Store struct {
	pool *pgxpool.Pool
}

// New 构建连接池；MaxConns 需与 pgBouncer / PG 连接上限匹配。
func New(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Close 释放连接池。
func (s *Store) Close() {
	s.pool.Close()
}

// Ping 用于健康检查。
func (s *Store) Ping(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.pool.Ping(c)
}

// LatestVersion 返回该语种最近一次构建成功的版本。
func (s *Store) LatestVersion(ctx context.Context, lang string) (*Version, error) {
	const q = `
SELECT version, lang, COALESCE(db_url, ''), COALESCE(vector_snapshot_url, ''),
       COALESCE(db_md5, ''), COALESCE(vector_md5, ''), status,
       COALESCE(source_commit, ''), created_at
FROM build_versions
WHERE lang = $1 AND status = 'built'
ORDER BY created_at DESC
LIMIT 1`
	var v Version
	err := s.pool.QueryRow(ctx, q, lang).Scan(
		&v.Version, &v.Lang, &v.DBURL, &v.VectorURL, &v.DBMD5, &v.VectorMD5, &v.Status, &v.SourceCommit, &v.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &v, nil
}

// StartBuild 登记一次构建（building 状态）。
func (s *Store) StartBuild(ctx context.Context, version, lang string) error {
	const q = `
INSERT INTO build_versions (version, lang, status)
VALUES ($1, $2, 'building')
ON CONFLICT (version) DO UPDATE SET lang = EXCLUDED.lang, status = 'building'`
	_, err := s.pool.Exec(ctx, q, version, lang)
	return err
}

// MarkFailed 只把未成功的记录置为 failed，避免覆盖 Python 已写入的成功结果。
func (s *Store) MarkFailed(ctx context.Context, version string) error {
	const q = `UPDATE build_versions SET status = 'failed' WHERE version = $1 AND status <> 'built'`
	_, err := s.pool.Exec(ctx, q, version)
	return err
}

// AppendLog 写入一条构建日志。
func (s *Store) AppendLog(ctx context.Context, version, stage, level, message string) error {
	const q = `INSERT INTO build_logs (version, stage, message, level) VALUES ($1, $2, $3, $4)`
	_, err := s.pool.Exec(ctx, q, version, stage, message, level)
	return err
}

// BuildStatus 返回版本详情与最近日志。
func (s *Store) BuildStatus(ctx context.Context, version string) (*Version, []LogEntry, error) {
	const vq = `
SELECT version, lang, COALESCE(db_url, ''), COALESCE(vector_snapshot_url, ''),
       COALESCE(db_md5, ''), COALESCE(vector_md5, ''), status,
       COALESCE(source_commit, ''), created_at
FROM build_versions WHERE version = $1`
	var v Version
	err := s.pool.QueryRow(ctx, vq, version).Scan(
		&v.Version, &v.Lang, &v.DBURL, &v.VectorURL, &v.DBMD5, &v.VectorMD5, &v.Status, &v.SourceCommit, &v.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	const lq = `
SELECT stage, COALESCE(level, 'info'), message, created_at
FROM build_logs WHERE version = $1 ORDER BY log_id DESC LIMIT 200`
	rows, err := s.pool.Query(ctx, lq, version)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	logs := make([]LogEntry, 0, 32)
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.Stage, &e.Level, &e.Message, &e.CreatedAt); err != nil {
			return nil, nil, err
		}
		logs = append(logs, e)
	}
	return &v, logs, rows.Err()
}
