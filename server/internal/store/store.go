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
