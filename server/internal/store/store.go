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
	DeletedAt    *time.Time `gorm:"column:deleted_at" json:"-"`
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

// ---------------------------------------------------------------------------
// 关卡配置（levels / level_exams）
// ---------------------------------------------------------------------------

// Level 对应 levels 一行：管理员维护的学习关卡。
type Level struct {
	ID          int64     `gorm:"primaryKey;column:id" json:"id"`
	Name        string    `gorm:"column:name" json:"name"`
	Lang        string    `gorm:"column:lang" json:"lang"`
	Description string    `gorm:"column:description" json:"description"`
	SortOrder   int       `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Level) TableName() string { return "levels" }

// LevelExam 对应 level_exams 一行：关卡与听力 exam 的关联。
type LevelExam struct {
	ID        int64     `gorm:"primaryKey;column:id" json:"-"`
	LevelID   int64     `gorm:"column:level_id" json:"level_id"`
	ExamID    string    `gorm:"column:exam_id" json:"exam_id"`
	SortOrder int       `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (LevelExam) TableName() string { return "level_exams" }

// LevelExamInfo 是关卡关联的 exam 附带 exam 基本信息的视图。
type LevelExamInfo struct {
	ExamID    string    `gorm:"column:exam_id" json:"exam_id"`
	Lang      string    `gorm:"column:lang" json:"lang"`
	SourceURL string    `gorm:"column:source_url" json:"source_url"`
	SortOrder int       `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

// CreateLevel 创建一个关卡，返回新 ID。
func (s *Store) CreateLevel(ctx context.Context, name, lang, description string, sortOrder int) (int64, error) {
	lv := Level{Name: name, Lang: lang, Description: description, SortOrder: sortOrder}
	if err := s.db.WithContext(ctx).Create(&lv).Error; err != nil {
		return 0, err
	}
	return lv.ID, nil
}

// UpdateLevel 更新关卡的名称、描述、排序。
func (s *Store) UpdateLevel(ctx context.Context, id int64, name, description string, sortOrder int) error {
	return s.db.WithContext(ctx).Model(&Level{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"name":        name,
			"description": description,
			"sort_order":  sortOrder,
			"updated_at":  gorm.Expr("now()"),
		}).Error
}

// DeleteLevel 删除关卡（级联删除 level_exams）。
func (s *Store) DeleteLevel(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Delete(&Level{}, id).Error
}

// GetLevel 返回单个关卡。
func (s *Store) GetLevel(ctx context.Context, id int64) (*Level, error) {
	var lv Level
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&lv).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &lv, nil
}

// ListLevels 返回全部关卡，按 sort_order、id 排序。
func (s *Store) ListLevels(ctx context.Context) ([]Level, error) {
	levels := make([]Level, 0)
	err := s.db.WithContext(ctx).
		Order("sort_order ASC, id ASC").
		Find(&levels).Error
	return levels, err
}

// ListLevelExams 返回关卡关联的 exam 列表（附带 exam 的 lang/source_url），按 sort_order 排序。
// 已软删除的 exam 不返回。
func (s *Store) ListLevelExams(ctx context.Context, levelID int64) ([]LevelExamInfo, error) {
	rows := make([]LevelExamInfo, 0)
	err := s.db.WithContext(ctx).
		Table("level_exams le").
		Select("le.exam_id, le.sort_order, le.created_at, j.lang, j.source_url").
		Joins("JOIN listening_jobs j ON j.exam_id = le.exam_id AND j.deleted_at IS NULL").
		Where("le.level_id = ?", levelID).
		Order("le.sort_order ASC, le.id ASC").
		Scan(&rows).Error
	return rows, err
}

// AddLevelExam 给关卡添加一个 exam；已存在则更新 sort_order。
func (s *Store) AddLevelExam(ctx context.Context, levelID int64, examID string, sortOrder int) error {
	row := LevelExam{LevelID: levelID, ExamID: examID, SortOrder: sortOrder}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "level_id"}, {Name: "exam_id"}},
		DoUpdates: clause.Assignments(map[string]any{"sort_order": sortOrder}),
	}).Create(&row).Error
}

// RemoveLevelExam 从关卡移除一个 exam。
func (s *Store) RemoveLevelExam(ctx context.Context, levelID int64, examID string) error {
	return s.db.WithContext(ctx).
		Where("level_id = ? AND exam_id = ?", levelID, examID).
		Delete(&LevelExam{}).Error
}

// AvailableExam 是可加入关卡的听力 exam（已构建完成）。
type AvailableExam struct {
	ExamID    string    `gorm:"column:exam_id" json:"exam_id"`
	Lang      string    `gorm:"column:lang" json:"lang"`
	SourceURL string    `gorm:"column:source_url" json:"source_url"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

// ListAvailableExams 返回所有已构建完成、且未被指定关卡关联、未软删除的听力 exam。
func (s *Store) ListAvailableExams(ctx context.Context, levelID int64) ([]AvailableExam, error) {
	rows := make([]AvailableExam, 0)
	q := s.db.WithContext(ctx).Table("listening_jobs").
		Select("DISTINCT exam_id, lang, source_url, created_at").
		Where("status = ? AND deleted_at IS NULL", "built").
		Where("exam_id NOT IN (?)",
			s.db.Table("level_exams").Select("exam_id").Where("level_id = ?", levelID)).
		Order("created_at DESC")
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

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

// SoftDeleteListeningJob 软删除听力任务（标记 deleted_at，不删除文件）。
func (s *Store) SoftDeleteListeningJob(ctx context.Context, jobID string) error {
	return s.db.WithContext(ctx).Model(&ListeningJob{}).
		Where("job_id = ? AND deleted_at IS NULL", jobID).
		Updates(map[string]any{
			"deleted_at": gorm.Expr("now()"),
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

// ListListeningJobs 返回最近 N 条任务（已软删除的除外）。
func (s *Store) ListListeningJobs(ctx context.Context, limit int) ([]ListeningJob, error) {
	if limit <= 0 {
		limit = 50
	}
	jobs := make([]ListeningJob, 0)
	err := s.db.WithContext(ctx).Table("listening_jobs").
		Select(`job_id, exam_id, lang, source_url, status,
			COALESCE(manifest_path,'') AS manifest_path,
			COALESCE(log_path,'') AS log_path,
			COALESCE(error_msg,'') AS error_msg,
			last_log_at, created_at, updated_at`).
		Where("deleted_at IS NULL").
		Order("updated_at DESC").
		Limit(limit).
		Find(&jobs).Error
	return jobs, err
}

// ListeningJobDetail 返回单个任务及其最近日志（已软删除的返回 not found）。
func (s *Store) ListeningJobDetail(ctx context.Context, jobID string) (*ListeningJob, []ListeningJobLog, error) {
	var j ListeningJob
	err := s.db.WithContext(ctx).Table("listening_jobs").
		Select(`job_id, exam_id, lang, source_url, status,
			COALESCE(manifest_path,'') AS manifest_path,
			COALESCE(log_path,'') AS log_path,
			COALESCE(error_msg,'') AS error_msg,
			last_log_at, created_at, updated_at`).
		Where("job_id = ? AND deleted_at IS NULL", jobID).
		Take(&j).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	logs := make([]ListeningJobLog, 0)
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

	logs := make([]LogEntry, 0)
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
