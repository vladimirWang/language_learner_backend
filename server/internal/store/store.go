// Package store 封装构建期 PostgreSQL 访问，仅用于版本清单与构建日志。
package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Version 对应 build_versions 一行；URL 字段为对象存储 key（本地模式下为文件路径）。
type Version struct {
	Version      string    `gorm:"primaryKey;column:version" json:"version"`
	Lang         string    `gorm:"column:lang" json:"lang"`
	DBURL        string    `gorm:"column:db_url" json:"db_url"`
	VectorURL    string    `gorm:"column:vector_snapshot_url" json:"vector_snapshot_url"`
	DBMD5        string    `gorm:"column:db_md5" json:"db_md5"`
	VectorMD5    string    `gorm:"column:vector_md5" json:"vector_md5"`
	Status       string    `gorm:"column:status" json:"status"`
	SourceCommit string    `gorm:"column:source_commit" json:"source_commit"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
}

func (Version) TableName() string { return "build_versions" }

// ListeningJob 对应 listening_jobs 一行。
type ListeningJob struct {
	JobID        string     `gorm:"primaryKey;column:job_id" json:"job_id"`
	ExamID       string     `gorm:"column:exam_id" json:"exam_id"`
	Lang         string     `gorm:"column:lang" json:"lang"`
	SourceURL    string     `gorm:"column:source_url" json:"source_url"`
	Status       string     `gorm:"column:status" json:"status"`
	ManifestPath string     `gorm:"column:manifest_path" json:"manifest_path"`
	LogPath      string     `gorm:"column:log_path" json:"log_path"`
	ErrorMsg     string     `gorm:"column:error_msg" json:"error_msg"`
	LastLogAt    *time.Time `gorm:"column:last_log_at" json:"last_log_at"`
	CreatedAt    time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (ListeningJob) TableName() string { return "listening_jobs" }

// ListeningJobLog 对应 listening_job_logs 一行。
type ListeningJobLog struct {
	JobID     string    `gorm:"column:job_id" json:"-"`
	Stage     string    `gorm:"column:stage" json:"stage"`
	Level     string    `gorm:"column:level" json:"level"`
	Message   string    `gorm:"column:message" json:"message"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (ListeningJobLog) TableName() string { return "listening_job_logs" }

// LogEntry 对应 build_logs 一行。
type LogEntry struct {
	Version   string    `gorm:"column:version" json:"-"`
	Stage     string    `gorm:"column:stage" json:"stage"`
	Level     string    `gorm:"column:level" json:"level"`
	Message   string    `gorm:"column:message" json:"message"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (LogEntry) TableName() string { return "build_logs" }

// Store 持有 gorm 数据库连接。
type Store struct {
	db *gorm.DB
}

// New 构建 gorm 连接；PostgreSQL 驱动底层使用 pgx。
func New(_ context.Context, dsn string, maxConns int32) (*Store, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(int(maxConns))
	sqlDB.SetMaxIdleConns(1)
	return &Store{db: db}, nil
}

// Close 释放连接池。
func (s *Store) Close() {
	if s == nil || s.db == nil {
		return
	}
	sqlDB, err := s.db.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
}

// Ping 用于健康检查。
func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return sqlDB.PingContext(c)
}

// ---------------------------------------------------------------------------
// 听力导入任务
// ---------------------------------------------------------------------------

// CreateListeningJob 登记一次听力导入任务。
func (s *Store) CreateListeningJob(ctx context.Context, jobID, examID, lang, sourceURL string) error {
	job := ListeningJob{
		JobID:     jobID,
		ExamID:    examID,
		Lang:      lang,
		SourceURL: sourceURL,
		Status:    "pending",
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "job_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"exam_id":       job.ExamID,
			"lang":          job.Lang,
			"source_url":    job.SourceURL,
			"status":        "pending",
			"error_msg":     nil,
			"manifest_path": nil,
			"log_path":      nil,
			"updated_at":    gorm.Expr("now()"),
		}),
	}).Create(&job).Error
}

// StartListeningJob 把状态置为 running。
func (s *Store) StartListeningJob(ctx context.Context, jobID string) error {
	return s.db.WithContext(ctx).Model(&ListeningJob{}).
		Where("job_id = ?", jobID).
		Updates(map[string]any{"status": "running", "updated_at": gorm.Expr("now()")}).Error
}

// FinishListeningJob 写入成功结果（manifest_path）。
func (s *Store) FinishListeningJob(ctx context.Context, jobID, manifestPath string) error {
	return s.db.WithContext(ctx).Model(&ListeningJob{}).
		Where("job_id = ?", jobID).
		Updates(map[string]any{
			"status":        "built",
			"manifest_path": manifestPath,
			"updated_at":    gorm.Expr("now()"),
		}).Error
}

// FailListeningJob 写入失败原因。
func (s *Store) FailListeningJob(ctx context.Context, jobID, errorMsg string) error {
	return s.db.WithContext(ctx).Model(&ListeningJob{}).
		Where("job_id = ?", jobID).
		Updates(map[string]any{
			"status":     "failed",
			"error_msg":  errorMsg,
			"updated_at": gorm.Expr("now()"),
		}).Error
}

// AppendListeningLog 追加一条任务日志，同时刷新 listening_jobs.last_log_at。
func (s *Store) AppendListeningLog(ctx context.Context, jobID, stage, level, message string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ListeningJobLog{
			JobID: jobID, Stage: stage, Level: level, Message: message,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&ListeningJob{}).Where("job_id = ?", jobID).
			Update("last_log_at", gorm.Expr("now()")).Error
	})
}

// ListListeningJobs 返回最近 N 条任务。
func (s *Store) ListListeningJobs(ctx context.Context, limit int) ([]ListeningJob, error) {
	if limit <= 0 {
		limit = 50
	}
	var jobs []ListeningJob
	err := s.db.WithContext(ctx).Table("listening_jobs").
		Select(`job_id, exam_id, lang, source_url, status,
			COALESCE(manifest_path,'') AS manifest_path,
			COALESCE(log_path,'') AS log_path,
			COALESCE(error_msg,'') AS error_msg,
			last_log_at, created_at, updated_at`).
		Order("updated_at DESC").
		Limit(limit).
		Find(&jobs).Error
	return jobs, err
}

// ListeningJobDetail 返回单个任务及其最近日志。
func (s *Store) ListeningJobDetail(ctx context.Context, jobID string) (*ListeningJob, []ListeningJobLog, error) {
	var j ListeningJob
	err := s.db.WithContext(ctx).Table("listening_jobs").
		Select(`job_id, exam_id, lang, source_url, status,
			COALESCE(manifest_path,'') AS manifest_path,
			COALESCE(log_path,'') AS log_path,
			COALESCE(error_msg,'') AS error_msg,
			last_log_at, created_at, updated_at`).
		Where("job_id = ?", jobID).
		Take(&j).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var logs []ListeningJobLog
	err = s.db.WithContext(ctx).Table("listening_job_logs").
		Select(`COALESCE(stage,'') AS stage, COALESCE(level,'') AS level, message, created_at`).
		Where("job_id = ?", jobID).
		Order("log_id DESC").
		Limit(300).
		Find(&logs).Error
	if err != nil {
		return nil, nil, err
	}
	return &j, logs, nil
}

// ---------------------------------------------------------------------------
// 构建任务（build_versions / build_logs）
// ---------------------------------------------------------------------------

// LatestVersion 返回该语种最近一次构建成功的版本。
func (s *Store) LatestVersion(ctx context.Context, lang string) (*Version, error) {
	var v Version
	err := s.db.WithContext(ctx).Table("build_versions").
		Select(`version, lang,
			COALESCE(db_url,'') AS db_url,
			COALESCE(vector_snapshot_url,'') AS vector_snapshot_url,
			COALESCE(db_md5,'') AS db_md5,
			COALESCE(vector_md5,'') AS vector_md5,
			status, COALESCE(source_commit,'') AS source_commit,
			created_at`).
		Where("lang = ? AND status = ?", lang, "built").
		Order("created_at DESC").
		Limit(1).
		Take(&v).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &v, nil
}

// StartBuild 登记一次构建（building 状态）。
func (s *Store) StartBuild(ctx context.Context, version, lang string) error {
	v := Version{Version: version, Lang: lang, Status: "building"}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "version"}},
		DoUpdates: clause.AssignmentColumns([]string{"lang", "status"}),
	}).Create(&v).Error
}

// MarkFailed 只把未成功的记录置为 failed，避免覆盖 Python 已写入的成功结果。
func (s *Store) MarkFailed(ctx context.Context, version string) error {
	return s.db.WithContext(ctx).Model(&Version{}).
		Where("version = ? AND status <> ?", version, "built").
		Update("status", "failed").Error
}

// AppendLog 写入一条构建日志。
func (s *Store) AppendLog(ctx context.Context, version, stage, level, message string) error {
	return s.db.WithContext(ctx).Create(&LogEntry{
		Version: version, Stage: stage, Level: level, Message: message,
	}).Error
}

// BuildStatus 返回版本详情与最近日志。
func (s *Store) BuildStatus(ctx context.Context, version string) (*Version, []LogEntry, error) {
	var v Version
	err := s.db.WithContext(ctx).Table("build_versions").
		Select(`version, lang,
			COALESCE(db_url,'') AS db_url,
			COALESCE(vector_snapshot_url,'') AS vector_snapshot_url,
			COALESCE(db_md5,'') AS db_md5,
			COALESCE(vector_md5,'') AS vector_md5,
			status, COALESCE(source_commit,'') AS source_commit,
			created_at`).
		Where("version = ?", version).
		Take(&v).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var logs []LogEntry
	err = s.db.WithContext(ctx).Table("build_logs").
		Select("stage, COALESCE(level,'info') AS level, message, created_at").
		Where("version = ?", version).
		Order("log_id DESC").
		Limit(200).
		Find(&logs).Error
	if err != nil {
		return nil, nil, err
	}
	return &v, logs, nil
}
