// Package store 封装构建期 PostgreSQL 访问，仅用于版本清单与构建日志。
package store

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

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
	Name         *string    `gorm:"column:name" json:"name"`
	Lang         string     `gorm:"column:lang" json:"lang"`
	Level        string     `gorm:"column:level" json:"level"`
	SourceURL    string     `gorm:"column:source_url" json:"source_url"`
	Status       string     `gorm:"column:status" json:"status"`
	ManifestPath string     `gorm:"column:manifest_path" json:"manifest_path"`
	LogPath      string     `gorm:"column:log_path" json:"log_path"`
	ErrorMsg     string     `gorm:"column:error_msg" json:"error_msg"`
	LastLogAt    *time.Time `gorm:"column:last_log_at" json:"last_log_at"`
	DeletedAt    *time.Time `gorm:"column:deleted_at" json:"deleted_at"`
	HasAnswers   bool       `gorm:"column:has_answers" json:"has_answers"`
	CreatedAt    time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (ListeningJob) TableName() string { return "listening_jobs" }

// ListeningJobLog 对应 listening_job_logs 一行。
type ListeningJobLog struct {
	LogID     int64     `gorm:"primaryKey;column:log_id" json:"log_id"`
	JobID     string    `gorm:"column:job_id" json:"-"`
	Stage     string    `gorm:"column:stage" json:"stage"`
	Level     string    `gorm:"column:level" json:"level"`
	Message   string    `gorm:"column:message" json:"message"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (ListeningJobLog) TableName() string { return "listening_job_logs" }

// ListeningJobNotification 对应 listening_job_notifications 一行。
// 用于管理台"消息中心"：AI 听力生成、构建完成等系统事件。
type ListeningJobNotification struct {
	ID        int64      `gorm:"primaryKey;column:id" json:"id"`
	JobID     string     `gorm:"column:job_id;index" json:"job_id"`
	EventType string     `gorm:"column:event_type" json:"event_type"` // progress / success / error / info
	Title     string     `gorm:"column:title" json:"title"`
	Message   string     `gorm:"column:message" json:"message"`
	Payload   string     `gorm:"column:payload" json:"-"` // jsonb raw string
	CreatedAt time.Time  `gorm:"column:created_at" json:"created_at"`
	ReadAt    *time.Time `gorm:"column:read_at" json:"read_at"`
}

// NotificationPayload 返回解析后的 payload；解析失败或空值均返回 nil。
func (n *ListeningJobNotification) NotificationPayload() map[string]any {
	if n.Payload == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(n.Payload), &out); err != nil {
		return nil
	}
	return out
}

func (ListeningJobNotification) TableName() string { return "listening_job_notifications" }

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
// 测试配置（tests / test_items，原名 levels / level_exams）
// ---------------------------------------------------------------------------

// Level 对应 tests 一行：管理员维护的学习测试。
type Level struct {
	ID          int64     `gorm:"primaryKey;column:id" json:"id"`
	Name        string    `gorm:"column:name" json:"name"`
	Lang        string    `gorm:"column:lang" json:"lang"`
	Difficulty  string    `gorm:"column:difficulty" json:"difficulty"`
	Active      bool      `gorm:"column:active" json:"active"`
	Description string    `gorm:"column:description" json:"description"`
	SortOrder   int       `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Level) TableName() string { return "tests" }

// LevelExam 对应 test_items 一行：测试与题目（听力 exam / 口语题目）的关联。
type LevelExam struct {
	ID        int64     `gorm:"primaryKey;column:id" json:"-"`
	LevelID   int64     `gorm:"column:level_id" json:"level_id"`
	ExamID    string    `gorm:"column:exam_id" json:"exam_id"`
	ItemType  string    `gorm:"column:item_type" json:"item_type"`
	SortOrder int       `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (LevelExam) TableName() string { return "test_items" }

// SpeakingItem 对应 speaking_items 一行：口语固定问答题库。
type SpeakingItem struct {
	ItemKey        string `gorm:"primaryKey;column:item_key" json:"item_key"`
	Lang           string `gorm:"column:lang" json:"lang"`
	Level          string `gorm:"column:level" json:"level"`
	ExamPart       string `gorm:"column:exam_part" json:"exam_part"`
	Topic          string `gorm:"column:topic" json:"topic"`
	Role           string `gorm:"column:role" json:"role"`
	PromptDE       string `gorm:"column:prompt_de" json:"prompt_de"`
	PromptZH       string `gorm:"column:prompt_zh" json:"prompt_zh"`
	ExpectedPoints string `gorm:"column:expected_points" json:"expected_points"`
	// TargetGrammar 是该题能自然引出的语法点 key 列表（JSON 数组）。
	TargetGrammar json.RawMessage `gorm:"column:target_grammar" json:"target_grammar"`
	RefAnswer     string          `gorm:"column:ref_answer" json:"ref_answer"`
	ErrorNote     string          `gorm:"column:error_note" json:"error_note"`
	SortOrder     int             `gorm:"column:sort_order" json:"sort_order"`
	Active        bool            `gorm:"column:active" json:"active"`
	CreatedAt     time.Time       `gorm:"column:created_at" json:"created_at"`
	UpdatedAt     time.Time       `gorm:"column:updated_at" json:"updated_at"`
}

func (SpeakingItem) TableName() string { return "speaking_items" }

// ListSpeakingItems 返回口语题库，可按语种与等级过滤，按等级与 sort_order 排序。
func (s *Store) ListSpeakingItems(ctx context.Context, lang, level string, activeOnly bool) ([]SpeakingItem, error) {
	items := make([]SpeakingItem, 0)
	q := s.db.WithContext(ctx)
	if lang != "" {
		q = q.Where("lang = ?", lang)
	}
	if level != "" {
		q = q.Where("level = ?", level)
	}
	if activeOnly {
		q = q.Where("active = ?", true)
	}
	err := q.Order("level ASC, sort_order ASC, item_key ASC").Find(&items).Error
	return items, err
}

// SpeakingExamPart 对应 speaking_exam_parts 一行：口语考试的固定部分配置。
// A1/A2 的考试结构（时长、追问上限、辅助材料）由该表驱动。
type SpeakingExamPart struct {
	ID           int64           `gorm:"primaryKey;column:id" json:"id"`
	Lang         string          `gorm:"column:lang" json:"lang"`
	Level        string          `gorm:"column:level" json:"level"`
	PartKey      string          `gorm:"column:part_key" json:"part_key"`
	Title        string          `gorm:"column:title" json:"title"`
	Mode         string          `gorm:"column:mode" json:"mode"`
	DurationSec  int             `gorm:"column:duration_sec" json:"duration_sec"`
	MaxFollowups int             `gorm:"column:max_followups" json:"max_followups"`
	Role         string          `gorm:"column:role" json:"role"`
	Aid          json.RawMessage `gorm:"column:aid" json:"aid"`
	SortOrder    int             `gorm:"column:sort_order" json:"sort_order"`
	Active       bool            `gorm:"column:active" json:"active"`
}

func (SpeakingExamPart) TableName() string { return "speaking_exam_parts" }

// ListSpeakingExamParts 返回口语考试部分配置，可按语种与等级过滤；activeOnly 为 false 时含停用项。
func (s *Store) ListSpeakingExamParts(ctx context.Context, lang, level string, activeOnly bool) ([]SpeakingExamPart, error) {
	parts := make([]SpeakingExamPart, 0)
	q := s.db.WithContext(ctx)
	if activeOnly {
		q = q.Where("active = ?", true)
	}
	if lang != "" {
		q = q.Where("lang = ?", lang)
	}
	if level != "" {
		q = q.Where("level = ?", level)
	}
	err := q.Order("level ASC, sort_order ASC, part_key ASC").Find(&parts).Error
	return parts, err
}

// aidExpr 把辅助材料 JSON 按 jsonb 写入；空值与 null 都写成 NULL。
func aidExpr(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return gorm.Expr("NULL::jsonb")
	}
	return gorm.Expr("?::jsonb", string(raw))
}

// CreateSpeakingExamPart 新增考试部分配置，返回新 ID。
func (s *Store) CreateSpeakingExamPart(ctx context.Context, part SpeakingExamPart) (int64, error) {
	err := s.db.WithContext(ctx).Model(&SpeakingExamPart{}).Create(map[string]any{
		"lang":          part.Lang,
		"level":         part.Level,
		"part_key":      part.PartKey,
		"title":         part.Title,
		"mode":          part.Mode,
		"duration_sec":  part.DurationSec,
		"max_followups": part.MaxFollowups,
		"role":          part.Role,
		"aid":           aidExpr(part.Aid),
		"sort_order":    part.SortOrder,
		"active":        part.Active,
	}).Error
	if err != nil {
		return 0, err
	}
	var created SpeakingExamPart
	err = s.db.WithContext(ctx).
		Where("lang = ? AND level = ? AND part_key = ?", part.Lang, part.Level, part.PartKey).
		Take(&created).Error
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

// UpdateSpeakingExamPart 按 ID 全量更新考试部分配置。
func (s *Store) UpdateSpeakingExamPart(ctx context.Context, part SpeakingExamPart) error {
	res := s.db.WithContext(ctx).Model(&SpeakingExamPart{}).
		Where("id = ?", part.ID).
		Updates(map[string]any{
			"lang":          part.Lang,
			"level":         part.Level,
			"part_key":      part.PartKey,
			"title":         part.Title,
			"mode":          part.Mode,
			"duration_sec":  part.DurationSec,
			"max_followups": part.MaxFollowups,
			"role":          part.Role,
			"aid":           aidExpr(part.Aid),
			"sort_order":    part.SortOrder,
			"active":        part.Active,
			"updated_at":    gorm.Expr("now()"),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSpeakingExamPart 按 ID 删除考试部分配置。
func (s *Store) DeleteSpeakingExamPart(ctx context.Context, id int64) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&SpeakingExamPart{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrNotFound 表示目标记录不存在，供 handler 映射为 404。
var ErrNotFound = errors.New("record not found")

// StringList 是存为 JSON 文本的字符串数组，便于在 TEXT 列里保存多值标签。
type StringList []string

func (s *StringList) Scan(value any) error {
	var raw []byte
	switch v := value.(type) {
	case nil:
		*s = StringList{}
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		*s = StringList{}
		return nil
	}
	if len(raw) == 0 {
		*s = StringList{}
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		*s = StringList{}
		return nil
	}
	*s = StringList(out)
	return nil
}

func (s StringList) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	raw, err := json.Marshal([]string(s))
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

// HasTopic 判断该话术是否绑定了指定话题。
func (s StringList) HasTopic(topic string) bool {
	for _, item := range s {
		if item == topic {
			return true
		}
	}
	return false
}

// SpeakingReplyTemplate 是口语追问/搭档回应的兜底话术。
type SpeakingReplyTemplate struct {
	ID        int64      `gorm:"primaryKey" json:"id"`
	Lang      string     `json:"lang"`
	ExamPart  string     `gorm:"column:exam_part" json:"exam_part"`
	Role      string     `json:"role"`
	Text      string     `json:"text"`
	Topics    StringList `gorm:"column:topics" json:"topics"`
	Active    bool       `json:"active"`
	SortOrder int        `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (SpeakingReplyTemplate) TableName() string { return "speaking_reply_templates" }

// ListSpeakingReplyTemplates 返回兜底话术，按部分与排序返回。
func (s *Store) ListSpeakingReplyTemplates(ctx context.Context, activeOnly bool) ([]SpeakingReplyTemplate, error) {
	items := make([]SpeakingReplyTemplate, 0)
	q := s.db.WithContext(ctx)
	if activeOnly {
		q = q.Where("active = ?", true)
	}
	err := q.Order("exam_part ASC, role ASC, sort_order ASC, id ASC").Find(&items).Error
	return items, err
}

func (s *Store) CreateSpeakingReplyTemplate(ctx context.Context, item *SpeakingReplyTemplate) error {
	return s.db.WithContext(ctx).Create(item).Error
}

// GetSpeakingReplyTemplate 返回单条兜底话术。
func (s *Store) GetSpeakingReplyTemplate(ctx context.Context, id int64) (*SpeakingReplyTemplate, error) {
	var item SpeakingReplyTemplate
	err := s.db.WithContext(ctx).First(&item, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Store) UpdateSpeakingReplyTemplate(ctx context.Context, item *SpeakingReplyTemplate) error {
	res := s.db.WithContext(ctx).Model(&SpeakingReplyTemplate{}).
		Where("id = ?", item.ID).
		Updates(map[string]any{
			"exam_part":  item.ExamPart,
			"role":       item.Role,
			"text":       item.Text,
			"topics":     item.Topics,
			"active":     item.Active,
			"sort_order": item.SortOrder,
			"updated_at": time.Now(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteSpeakingReplyTemplate(ctx context.Context, id int64) error {
	res := s.db.WithContext(ctx).Delete(&SpeakingReplyTemplate{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSpeakingTopics 返回题库中出现过的话题名，供管理台做话术标签选择。
func (s *Store) ListSpeakingTopics(ctx context.Context, lang string) ([]string, error) {
	topics := make([]string, 0)
	q := s.db.WithContext(ctx).Table("speaking_items").
		Where("topic <> ''").
		Distinct("topic").
		Order("topic ASC")
	if lang != "" {
		q = q.Where("lang = ?", lang)
	}
	err := q.Pluck("topic", &topics).Error
	return topics, err
}

// MissingSpeakingTopics 返回在题库中不存在的话题名。
func (s *Store) MissingSpeakingTopics(ctx context.Context, topics []string) ([]string, error) {
	if len(topics) == 0 {
		return nil, nil
	}
	found := make([]string, 0, len(topics))
	if err := s.db.WithContext(ctx).Table("speaking_items").
		Where("topic IN ?", topics).
		Distinct("topic").
		Pluck("topic", &found).Error; err != nil {
		return nil, err
	}
	exists := make(map[string]bool, len(found))
	for _, topic := range found {
		exists[topic] = true
	}
	missing := make([]string, 0)
	for _, topic := range topics {
		if !exists[topic] {
			missing = append(missing, topic)
		}
	}
	return missing, nil
}

// SearchRagChunks 按关键词粗检索参考语料，用于口语追问的轻量 RAG 注入。
// 不在交互路径上加载 embedding 模型，只做 content ILIKE 命中。
func (s *Store) SearchRagChunks(ctx context.Context, lang, level string, keywords []string, limit int) ([]string, error) {
	contents := make([]string, 0)
	if len(keywords) == 0 {
		return contents, nil
	}
	if limit <= 0 || limit > 5 {
		limit = 3
	}
	tx := s.db.WithContext(ctx).Table("rag_chunks").Select("content").Where("lang = ?", lang)
	if level != "" {
		tx = tx.Where("(level = ? OR level IS NULL OR level = '')", level)
	}
	conds := make([]string, 0, len(keywords))
	args := make([]any, 0, len(keywords))
	for _, kw := range keywords {
		conds = append(conds, "content ILIKE ?")
		args = append(args, "%"+kw+"%")
	}
	tx = tx.Where("("+strings.Join(conds, " OR ")+")", args...)
	if err := tx.Order("chunk_id DESC").Limit(limit).Scan(&contents).Error; err != nil {
		return nil, err
	}
	return contents, nil
}

// GrammarChunk 是一条语法规范切片。
type GrammarChunk struct {
	Title   string
	Content string
}

// GrammarChunks 取出当前等级可用的语法规范，按关键词命中数排序后返回前 limit 条。
// 语法讲解以中文为主，关键词主要用于命中德语例句；命中不足时按等级兜底，
// 低等级没有对应文档时回退到全部语法文档。
func (s *Store) GrammarChunks(ctx context.Context, lang, level string, keywords []string, limit int) ([]GrammarChunk, error) {
	if limit <= 0 || limit > 6 {
		limit = 4
	}
	type row struct {
		ChunkID    int64
		Title      string
		Content    string
		SourcePath string
	}
	rows := make([]row, 0, 128)
	load := func(withLevel bool) error {
		tx := s.db.WithContext(ctx).Table("rag_chunks").
			Select("chunk_id, title, content, source_path").
			Where("lang = ? AND chunk_type = 'grammar'", lang)
		if withLevel && level != "" {
			tx = tx.Where("(level LIKE ? OR level IS NULL OR level = '')", "%"+level+"%")
		}
		return tx.Order("chunk_id ASC").Limit(200).Scan(&rows).Error
	}
	if err := load(true); err != nil {
		return nil, err
	}
	if len(rows) == 0 && level != "" {
		rows = rows[:0]
		if err := load(false); err != nil {
			return nil, err
		}
	}

	words := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		if k := strings.ToLower(strings.TrimSpace(kw)); k != "" {
			words = append(words, k)
		}
	}
	type scored struct {
		chunk  GrammarChunk
		source string
		score  int
		id     int64
	}
	list := make([]scored, 0, len(rows))
	for _, r := range rows {
		body := strings.ToLower(r.Title + " " + r.Content)
		score := 0
		for _, kw := range words {
			if strings.Contains(body, kw) {
				score++
			}
		}
		content := strings.Join(strings.Fields(r.Content), " ")
		if utf8.RuneCountInString(content) > 420 {
			content = string([]rune(content)[:420]) + "…"
		}
		// 切片标题只是二级标题，补上文档名才能让模型知道引用的是哪条规范。
		docName := strings.TrimSuffix(path.Base(r.SourcePath), ".md")
		title := r.Title
		if docName != "" {
			title = docName + " · " + r.Title
		}
		list = append(list, scored{
			chunk:  GrammarChunk{Title: title, Content: content},
			source: r.SourcePath,
			score:  score,
			id:     r.ChunkID,
		})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].id < list[j].id
	})
	// 无命中时每个文档只取一条，保证覆盖冠词、介词、语序等不同规则；
	// 有命中时允许同一文档最多取两条，保留上下文完整性。
	out := make([]GrammarChunk, 0, limit)
	used := make(map[string]int)
	for _, item := range list {
		capPerDoc := 1
		if item.score > 0 {
			capPerDoc = 2
		}
		if used[item.source] >= capPerDoc {
			continue
		}
		used[item.source]++
		out = append(out, item.chunk)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// GetSpeakingItem 返回单条口语题目。
func (s *Store) GetSpeakingItem(ctx context.Context, key string) (*SpeakingItem, error) {
	var item SpeakingItem
	err := s.db.WithContext(ctx).Where("item_key = ?", key).Take(&item).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}

// CreateSpeakingItem 新增口语题目。
func (s *Store) CreateSpeakingItem(ctx context.Context, item SpeakingItem) error {
	return s.db.WithContext(ctx).Create(&item).Error
}

// UpdateSpeakingItem 按 item_key 更新口语题目；记录不存在时返回 ErrRecordNotFound。
func (s *Store) UpdateSpeakingItem(ctx context.Context, item SpeakingItem) error {
	res := s.db.WithContext(ctx).Model(&SpeakingItem{}).
		Where("item_key = ?", item.ItemKey).
		Updates(map[string]any{
			"lang":            item.Lang,
			"level":           item.Level,
			"exam_part":       item.ExamPart,
			"topic":           item.Topic,
			"role":            item.Role,
			"prompt_de":       item.PromptDE,
			"prompt_zh":       item.PromptZH,
			"expected_points": item.ExpectedPoints,
			"target_grammar":  item.TargetGrammar,
			"ref_answer":      item.RefAnswer,
			"error_note":      item.ErrorNote,
			"sort_order":      item.SortOrder,
			"active":          item.Active,
			"updated_at":      gorm.Expr("now()"),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSpeakingItem 按 item_key 删除口语题目。
func (s *Store) DeleteSpeakingItem(ctx context.Context, key string) error {
	res := s.db.WithContext(ctx).Where("item_key = ?", key).Delete(&SpeakingItem{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// LevelExamInfo 是关卡关联的 exam 附带 exam 基本信息的视图。
type LevelExamInfo struct {
	ExamID    string    `gorm:"column:exam_id" json:"exam_id"`
	ItemType  string    `gorm:"column:item_type" json:"item_type"`
	Name      string    `gorm:"column:name" json:"name"`
	Lang      string    `gorm:"column:lang" json:"lang"`
	SourceURL string    `gorm:"column:source_url" json:"source_url"`
	SortOrder int       `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

// CreateLevel 创建一个关卡，返回新 ID。
func (s *Store) CreateLevel(ctx context.Context, name, lang, difficulty, description string, sortOrder int) (int64, error) {
	lv := Level{Name: name, Lang: lang, Difficulty: difficulty, Description: description, SortOrder: sortOrder}
	if err := s.db.WithContext(ctx).Create(&lv).Error; err != nil {
		return 0, err
	}
	return lv.ID, nil
}

// UpdateLevel 更新关卡的名称、难度、描述、排序、生效状态。
// 设置 active=true 时，要求该关卡已关联至少一个 exam。
func (s *Store) UpdateLevel(ctx context.Context, id int64, name, difficulty, description string, sortOrder int, active bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if active {
			var cnt int64
			if err := tx.Table("test_items").Where("level_id = ?", id).Count(&cnt).Error; err != nil {
				return err
			}
			if cnt == 0 {
				return errors.New("无法生效：该关卡还没有关联任何题目")
			}
		}
		return tx.Model(&Level{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"name":        name,
				"difficulty":  difficulty,
				"description": description,
				"sort_order":  sortOrder,
				"active":      active,
				"updated_at":  gorm.Expr("now()"),
			}).Error
	})
}

// SetLevelActive 单独切换关卡生效状态；生效时校验必须有关联题目。
func (s *Store) SetLevelActive(ctx context.Context, id int64, active bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if active {
			var cnt int64
			if err := tx.Table("test_items").Where("level_id = ?", id).Count(&cnt).Error; err != nil {
				return err
			}
			if cnt == 0 {
				return errors.New("无法生效：该关卡还没有关联任何题目")
			}
		}
		return tx.Model(&Level{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"active":     active,
				"updated_at": gorm.Expr("now()"),
			}).Error
	})
}

// DeleteLevel 删除测试（级联删除 test_items）。
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

// ListLevels 返回关卡列表，按 sort_order、id 排序。
// activeOnly=true 时只返回已生效的关卡（供 client 端使用）。
func (s *Store) ListLevels(ctx context.Context, activeOnly bool) ([]Level, error) {
	levels := make([]Level, 0)
	q := s.db.WithContext(ctx).Order("sort_order ASC, id ASC")
	if activeOnly {
		q = q.Where("active = ?", true)
	}
	err := q.Find(&levels).Error
	return levels, err
}

// ListSpeakingItemsByTopic 返回指定语种、等级、主题下的启用口语题。
func (s *Store) ListSpeakingItemsByTopic(ctx context.Context, lang, level, topic string) ([]SpeakingItem, error) {
	items := make([]SpeakingItem, 0)
	err := s.db.WithContext(ctx).
		Where("lang = ? AND level = ? AND topic = ? AND active = ?", lang, level, topic, true).
		Order("exam_part ASC, sort_order ASC, item_key ASC").
		Find(&items).Error
	return items, err
}

// SpeakingPlan 是同一难度下由若干口语题目组成的方案，可被多个关卡引用。
type SpeakingPlan struct {
	ID          int64  `gorm:"column:id;primaryKey" json:"id"`
	Lang        string `gorm:"column:lang" json:"lang"`
	Level       string `gorm:"column:level" json:"level"`
	Name        string `gorm:"column:name" json:"name"`
	Description string `gorm:"column:description" json:"description"`
	Active      bool   `gorm:"column:active" json:"active"`
	SortOrder   int    `gorm:"column:sort_order" json:"sort_order"`
	ItemCount   int64  `gorm:"column:item_count" json:"item_count"`
	// GrammarRequirements 是方案级语法要求，整场任意回答达成即可。
	GrammarRequirements JSONRaw   `gorm:"column:grammar_requirements" json:"grammar_requirements"`
	CreatedAt           time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt           time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (SpeakingPlan) TableName() string { return "speaking_exam_plans" }

// grammarRequirementsValue 空值按空数组写入，避免违反 NOT NULL 约束。
func grammarRequirementsValue(raw JSONRaw) string {
	if len(raw) == 0 {
		return "[]"
	}
	return string(raw)
}

// ListSpeakingPlans 返回口语方案列表，activeOnly 为 false 时含停用项。
func (s *Store) ListSpeakingPlans(ctx context.Context, lang, level string, activeOnly bool) ([]SpeakingPlan, error) {
	plans := make([]SpeakingPlan, 0)
	q := s.db.WithContext(ctx).
		Table("speaking_exam_plans p").
		Select(`p.id, p.lang, p.level, p.name, p.description, p.active, p.sort_order,
			p.grammar_requirements, p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM speaking_exam_plan_items i WHERE i.plan_id = p.id) AS item_count`)
	if activeOnly {
		q = q.Where("p.active = ?", true)
	}
	if lang != "" {
		q = q.Where("p.lang = ?", lang)
	}
	if level != "" {
		q = q.Where("p.level = ?", level)
	}
	err := q.Order("p.level ASC, p.sort_order ASC, p.id ASC").Scan(&plans).Error
	return plans, err
}

// GetSpeakingPlan 返回方案及其题目，题目按方案内顺序排列。
func (s *Store) GetSpeakingPlan(ctx context.Context, id int64) (*SpeakingPlan, []SpeakingItem, error) {
	var plan SpeakingPlan
	err := s.db.WithContext(ctx).
		Table("speaking_exam_plans p").
		Select(`p.id, p.lang, p.level, p.name, p.description, p.active, p.sort_order,
			p.grammar_requirements, p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM speaking_exam_plan_items i WHERE i.plan_id = p.id) AS item_count`).
		Where("p.id = ?", id).
		Take(&plan).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	items := make([]SpeakingItem, 0)
	err = s.db.WithContext(ctx).
		Table("speaking_items si").
		Select("si.*").
		Joins("JOIN speaking_exam_plan_items i ON i.item_key = si.item_key").
		Where("i.plan_id = ?", id).
		Order("i.sort_order ASC, si.item_key ASC").
		Scan(&items).Error
	return &plan, items, err
}

// CountSoloSpeakingItems 统计 keys 中符合难度要求的题目；启用方案时同时校验语种和题目状态。
func (s *Store) CountSoloSpeakingItems(ctx context.Context, lang, level string, keys []string, activeOnly bool) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	var count int64
	q := s.db.WithContext(ctx).Model(&SpeakingItem{}).
		Where("level = ? AND item_key IN ?", level, keys)
	if activeOnly {
		q = q.Where("lang = ? AND active = ?", lang, true)
	}
	err := q.Count(&count).Error
	return count, err
}

// replacePlanItems 覆盖方案题目映射。
func replacePlanItems(tx *gorm.DB, planID int64, keys []string) error {
	if err := tx.Exec("DELETE FROM speaking_exam_plan_items WHERE plan_id = ?", planID).Error; err != nil {
		return err
	}
	for index, key := range keys {
		if err := tx.Exec(
			`INSERT INTO speaking_exam_plan_items (plan_id, item_key, sort_order)
			 VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
			planID, key, index,
		).Error; err != nil {
			return err
		}
	}
	return nil
}

// createSpeakingPlan 在事务内创建方案及有序题目映射。
func createSpeakingPlan(tx *gorm.DB, plan SpeakingPlan, itemKeys []string) (int64, error) {
	if err := tx.Model(&SpeakingPlan{}).Create(map[string]any{
		"lang":                 plan.Lang,
		"level":                plan.Level,
		"name":                 plan.Name,
		"description":          plan.Description,
		"active":               plan.Active,
		"sort_order":           plan.SortOrder,
		"grammar_requirements": grammarRequirementsValue(plan.GrammarRequirements),
	}).Error; err != nil {
		return 0, err
	}
	var created SpeakingPlan
	if err := tx.Where("lang = ? AND level = ? AND name = ?", plan.Lang, plan.Level, plan.Name).
		Take(&created).Error; err != nil {
		return 0, err
	}
	if err := replacePlanItems(tx, created.ID, itemKeys); err != nil {
		return 0, err
	}
	return created.ID, nil
}

// CreateSpeakingPlan 新建方案及其题目，返回方案 ID。
func (s *Store) CreateSpeakingPlan(ctx context.Context, plan SpeakingPlan, itemKeys []string) (int64, error) {
	var id int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		id, err = createSpeakingPlan(tx, plan, itemKeys)
		return err
	})
	return id, err
}

// CreateGeneratedSpeakingPlan 原子写入智能生成的新题、停用方案及有序题目映射。
func (s *Store) CreateGeneratedSpeakingPlan(ctx context.Context, plan SpeakingPlan, itemKeys []string, newItems []SpeakingItem) (int64, error) {
	var id int64
	plan.Active = false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range newItems {
			newItems[i].Active = false
			if err := tx.Create(&newItems[i]).Error; err != nil {
				return err
			}
		}
		var err error
		id, err = createSpeakingPlan(tx, plan, itemKeys)
		return err
	})
	return id, err
}

// UpdateSpeakingPlan 全量更新方案与题目。
func (s *Store) UpdateSpeakingPlan(ctx context.Context, id int64, plan SpeakingPlan, itemKeys []string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&SpeakingPlan{}).Where("id = ?", id).Updates(map[string]any{
			"lang":                 plan.Lang,
			"level":                plan.Level,
			"name":                 plan.Name,
			"description":          plan.Description,
			"active":               plan.Active,
			"sort_order":           plan.SortOrder,
			"grammar_requirements": grammarRequirementsValue(plan.GrammarRequirements),
			"updated_at":           gorm.Expr("now()"),
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return replacePlanItems(tx, id, itemKeys)
	})
}

// DeleteSpeakingPlan 删除方案；题目映射随外键级联删除。
func (s *Store) DeleteSpeakingPlan(ctx context.Context, id int64) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&SpeakingPlan{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListLevelExams 返回关卡关联的内容，按 sort_order 排序。
// 口语以方案为单位返回（exam_id 即方案 ID），由客户端进入口语考试页。
// 已软删除的听力 exam 不返回。
func (s *Store) ListLevelExams(ctx context.Context, levelID int64) ([]LevelExamInfo, error) {
	rows := make([]LevelExamInfo, 0)
	// 听力 exam 与历史遗留的单题口语关联。
	if err := s.db.WithContext(ctx).
		Table("test_items le").
		Select(`le.exam_id, le.item_type, le.sort_order, le.created_at,
			COALESCE(j.lang, si.lang, '') AS lang,
			COALESCE(j.source_url, '') AS source_url,
			COALESCE(NULLIF(j.name, ''), si.prompt_de, '') AS name`).
		Joins("LEFT JOIN listening_jobs j ON le.item_type = 'listening' AND j.exam_id = le.exam_id AND j.deleted_at IS NULL").
		Joins("LEFT JOIN speaking_items si ON le.item_type = 'speaking' AND si.item_key = le.exam_id AND si.active = true").
		Where("le.level_id = ?", levelID).
		Where("(le.item_type = 'listening' AND j.exam_id IS NOT NULL) OR (le.item_type = 'speaking' AND si.item_key IS NOT NULL)").
		Order("le.sort_order ASC, le.id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	plans := make([]LevelExamInfo, 0)
	err := s.db.WithContext(ctx).
		Table("test_items le").
		Select(`le.exam_id, le.item_type, le.sort_order, le.created_at,
			COALESCE(p.lang, l.lang, '') AS lang, '' AS source_url,
			COALESCE(NULLIF(p.name, ''), le.exam_id) AS name`).
		Joins("LEFT JOIN tests l ON l.id = le.level_id").
		Joins("LEFT JOIN speaking_exam_plans p ON le.item_type = 'speaking_plan' AND p.id::text = le.exam_id AND p.active = true").
		Joins("LEFT JOIN speaking_items si_topic ON le.item_type = 'speaking_topic' AND si_topic.item_key = le.exam_id AND si_topic.active = true").
		Where(`le.level_id = ? AND
			(le.item_type = 'speaking_topic' AND si_topic.item_key IS NOT NULL)
			OR (le.item_type = 'speaking_plan' AND p.id IS NOT NULL)`, levelID).
		Order("le.sort_order ASC, le.id ASC").
		Scan(&plans).Error
	if err != nil {
		return nil, err
	}
	rows = append(rows, plans...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].SortOrder < rows[j].SortOrder })
	return rows, nil
}

// AddLevelExam 给关卡添加一个题目（听力或口语）；已存在则更新 sort_order。
func (s *Store) AddLevelExam(ctx context.Context, levelID int64, itemType, examID string, sortOrder int) error {
	row := LevelExam{LevelID: levelID, ExamID: examID, ItemType: itemType, SortOrder: sortOrder}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "level_id"}, {Name: "item_type"}, {Name: "exam_id"}},
		DoUpdates: clause.Assignments(map[string]any{"sort_order": sortOrder}),
	}).Create(&row).Error
}

// RemoveLevelExam 从关卡移除一个题目。
func (s *Store) RemoveLevelExam(ctx context.Context, levelID int64, itemType, examID string) error {
	return s.db.WithContext(ctx).
		Where("level_id = ? AND item_type = ? AND exam_id = ?", levelID, itemType, examID).
		Delete(&LevelExam{}).Error
}

// AvailableExam 是可加入关卡的题目：已构建完成且绑定答案的听力 exam，
// 或已填写要点（expected_points）的启用口语题目。
type AvailableExam struct {
	ExamID    string    `gorm:"column:exam_id" json:"exam_id"`
	ItemType  string    `gorm:"column:item_type" json:"item_type"`
	Name      *string   `gorm:"column:name" json:"name"`
	Lang      string    `gorm:"column:lang" json:"lang"`
	SourceURL string    `gorm:"column:source_url" json:"source_url"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

// ListAvailableExams 返回可加入指定关卡且尚未关联的听力 exam 与口语方案。
// 口语以方案为单位：只返回该关卡语种与难度下、启用且含题目的方案。
func (s *Store) ListAvailableExams(ctx context.Context, levelID int64) ([]AvailableExam, error) {
	var lv Level
	if err := s.db.WithContext(ctx).Where("id = ?", levelID).Take(&lv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []AvailableExam{}, nil
		}
		return nil, err
	}
	rows := make([]AvailableExam, 0)
	err := s.db.WithContext(ctx).Raw(`
		SELECT 'listening' AS item_type, exam_id, name, lang, source_url, created_at
		FROM listening_jobs
		WHERE status = 'built' AND deleted_at IS NULL AND has_answers = true
		  AND exam_id NOT IN (
		      SELECT exam_id FROM test_items WHERE level_id = ? AND item_type = 'listening'
		  )
		UNION ALL
		SELECT 'speaking_plan' AS item_type, p.id::text AS exam_id,
		       p.name || '（' || (SELECT COUNT(*) FROM speaking_exam_plan_items i WHERE i.plan_id = p.id)::text || ' 题）' AS name,
		       p.lang, '' AS source_url, p.created_at
		FROM speaking_exam_plans p
		WHERE p.active = true AND p.lang = ? AND p.level = ?
		  AND EXISTS (SELECT 1 FROM speaking_exam_plan_items i WHERE i.plan_id = p.id)
		  AND p.id::text NOT IN (
		      SELECT exam_id FROM test_items WHERE level_id = ? AND item_type = 'speaking_plan'
		  )
		ORDER BY item_type ASC, created_at DESC
	`, levelID, lv.Lang, lv.Difficulty, levelID).Scan(&rows).Error
	return rows, err
}

// PDFImportJob 记录一次 PDF 试卷导入：文件先落盘登记，再异步解析。
type PDFImportJob struct {
	JobID        string          `gorm:"column:job_id;primaryKey" json:"job_id"`
	OriginalName string          `gorm:"column:original_name" json:"original_name"`
	StoredPath   string          `gorm:"column:stored_path" json:"stored_path"`
	SizeBytes    int64           `gorm:"column:size_bytes" json:"size_bytes"`
	FileMD5      string          `gorm:"column:file_md5" json:"file_md5"`
	Lang         string          `gorm:"column:lang" json:"lang"`
	Status       string          `gorm:"column:status" json:"status"`
	Message      string          `gorm:"column:message" json:"message"`
	PageCount    int             `gorm:"column:page_count" json:"page_count"`
	Result       json.RawMessage `gorm:"column:result_json" json:"result"`
	CreatedAt    time.Time       `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time       `gorm:"column:updated_at" json:"updated_at"`
	StartedAt    *time.Time      `gorm:"column:started_at" json:"started_at"`
	FinishedAt   *time.Time      `gorm:"column:finished_at" json:"finished_at"`
	DeletedAt    *time.Time      `gorm:"column:deleted_at" json:"deleted_at"`
}

func (PDFImportJob) TableName() string { return "pdf_import_jobs" }

// PDFImportLog 是 PDF 解析子进程的一行日志。
type PDFImportLog struct {
	LogID     int64     `gorm:"column:log_id;primaryKey" json:"log_id"`
	JobID     string    `gorm:"column:job_id" json:"job_id"`
	Level     string    `gorm:"column:level" json:"level"`
	Message   string    `gorm:"column:message" json:"message"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (PDFImportLog) TableName() string { return "pdf_import_logs" }

// PDFUploadSession 记录一次分片上传，fingerprint 用于断点续传。
type PDFUploadSession struct {
	UploadID     string    `gorm:"column:upload_id;primaryKey" json:"upload_id"`
	Fingerprint  string    `gorm:"column:fingerprint" json:"fingerprint"`
	OriginalName string    `gorm:"column:original_name" json:"original_name"`
	SizeBytes    int64     `gorm:"column:size_bytes" json:"size_bytes"`
	Lang         string    `gorm:"column:lang" json:"lang"`
	ChunkSize    int       `gorm:"column:chunk_size" json:"chunk_size"`
	TotalChunks  int       `gorm:"column:total_chunks" json:"total_chunks"`
	Status       string    `gorm:"column:status" json:"status"`
	JobID        *string   `gorm:"column:job_id" json:"job_id"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (PDFUploadSession) TableName() string { return "pdf_upload_sessions" }

// CreatePDFUploadSession 登记分片上传会话。
func (s *Store) CreatePDFUploadSession(ctx context.Context, session PDFUploadSession) error {
	return s.db.WithContext(ctx).Model(&PDFUploadSession{}).Create(map[string]any{
		"upload_id":     session.UploadID,
		"fingerprint":   session.Fingerprint,
		"original_name": session.OriginalName,
		"size_bytes":    session.SizeBytes,
		"lang":          session.Lang,
		"chunk_size":    session.ChunkSize,
		"total_chunks":  session.TotalChunks,
		"status":        "uploading",
	}).Error
}

// GetPDFUploadSession 按 upload_id 查询；不存在返回 nil。
func (s *Store) GetPDFUploadSession(ctx context.Context, uploadID string) (*PDFUploadSession, error) {
	var session PDFUploadSession
	err := s.db.WithContext(ctx).Where("upload_id = ?", uploadID).Take(&session).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

// GetPDFUploadSessionByFingerprint 按文件指纹查询会话，用于断点续传。
func (s *Store) GetPDFUploadSessionByFingerprint(ctx context.Context, fingerprint string) (*PDFUploadSession, error) {
	var session PDFUploadSession
	err := s.db.WithContext(ctx).Where("fingerprint = ?", fingerprint).Take(&session).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

// ListPDFUploadChunks 返回已收到的分片序号。
func (s *Store) ListPDFUploadChunks(ctx context.Context, uploadID string) ([]int, error) {
	indexes := make([]int, 0)
	err := s.db.WithContext(ctx).
		Table("pdf_upload_chunks").
		Where("upload_id = ?", uploadID).
		Order("chunk_index ASC").
		Pluck("chunk_index", &indexes).Error
	return indexes, err
}

// UpsertPDFUploadChunk 记录一个已上传分片（重复上传按覆盖处理）。
func (s *Store) UpsertPDFUploadChunk(ctx context.Context, uploadID string, index int, size int64) error {
	return s.db.WithContext(ctx).Exec(
		`INSERT INTO pdf_upload_chunks (upload_id, chunk_index, size_bytes)
		 VALUES (?, ?, ?)
		 ON CONFLICT (upload_id, chunk_index) DO UPDATE SET size_bytes = EXCLUDED.size_bytes`,
		uploadID, index, size,
	).Error
}

// CompletePDFUploadSession 标记会话完成并关联导入任务。
func (s *Store) CompletePDFUploadSession(ctx context.Context, uploadID, jobID string) error {
	return s.db.WithContext(ctx).Model(&PDFUploadSession{}).Where("upload_id = ?", uploadID).
		Updates(map[string]any{
			"status":     "completed",
			"job_id":     jobID,
			"updated_at": gorm.Expr("now()"),
		}).Error
}

// FindPDFImportJobByMD5 按文件 MD5 查找已有导入任务，用于去重。
func (s *Store) FindPDFImportJobByMD5(ctx context.Context, md5sum, lang string) (*PDFImportJob, error) {
	var job PDFImportJob
	err := s.db.WithContext(ctx).
		Where("file_md5 = ? AND lang = ? AND file_md5 <> '' AND deleted_at IS NULL", md5sum, lang).
		Order("created_at DESC").
		Take(&job).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

// CreatePDFImportJob 登记一个待解析任务。
func (s *Store) CreatePDFImportJob(ctx context.Context, job PDFImportJob) error {
	return s.db.WithContext(ctx).Model(&PDFImportJob{}).Create(map[string]any{
		"job_id":        job.JobID,
		"original_name": job.OriginalName,
		"stored_path":   job.StoredPath,
		"size_bytes":    job.SizeBytes,
		"file_md5":      job.FileMD5,
		"lang":          job.Lang,
		"status":        job.Status,
		"message":       job.Message,
	}).Error
}

// GetPDFImportJob 返回单个任务；不存在返回 nil。
func (s *Store) GetPDFImportJob(ctx context.Context, jobID string) (*PDFImportJob, error) {
	var job PDFImportJob
	err := s.db.WithContext(ctx).Where("job_id = ?", jobID).Take(&job).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

// ListPDFImportJobs 返回最近的导入任务；默认不包含已软删除的任务。
func (s *Store) ListPDFImportJobs(ctx context.Context, limit int, includeDeleted bool) ([]PDFImportJob, error) {
	jobs := make([]PDFImportJob, 0)
	q := s.db.WithContext(ctx).Order("created_at DESC").Limit(limit)
	if !includeDeleted {
		q = q.Where("deleted_at IS NULL")
	}
	err := q.Find(&jobs).Error
	return jobs, err
}

// SoftDeletePDFImportJob 软删除任务；文件与解析结果保留，可恢复。
func (s *Store) SoftDeletePDFImportJob(ctx context.Context, jobID string) error {
	return s.db.WithContext(ctx).Model(&PDFImportJob{}).
		Where("job_id = ? AND deleted_at IS NULL", jobID).
		Updates(map[string]any{
			"deleted_at": gorm.Expr("now()"),
			"updated_at": gorm.Expr("now()"),
		}).Error
}

// RestorePDFImportJob 恢复已软删除的任务。
func (s *Store) RestorePDFImportJob(ctx context.Context, jobID string) error {
	return s.db.WithContext(ctx).Model(&PDFImportJob{}).Where("job_id = ?", jobID).
		Updates(map[string]any{
			"deleted_at": nil,
			"updated_at": gorm.Expr("now()"),
		}).Error
}

// ListPDFImportLogs 返回任务日志；afterLogID > 0 时只返回更新的部分。
func (s *Store) ListPDFImportLogs(ctx context.Context, jobID string, afterLogID int64) ([]PDFImportLog, error) {
	logs := make([]PDFImportLog, 0)
	q := s.db.WithContext(ctx).Where("job_id = ?", jobID)
	if afterLogID > 0 {
		q = q.Where("log_id > ?", afterLogID)
	}
	err := q.Order("log_id ASC").Find(&logs).Error
	return logs, err
}

// AppendPDFImportLog 追加一行解析日志。
func (s *Store) AppendPDFImportLog(ctx context.Context, jobID, level, message string) error {
	return s.db.WithContext(ctx).Model(&PDFImportLog{}).Create(map[string]any{
		"job_id":  jobID,
		"level":   level,
		"message": message,
	}).Error
}

// MarkPDFImportQueued 标记消息已成功投递。
func (s *Store) MarkPDFImportQueued(ctx context.Context, jobID string) error {
	// 只在仍为 pending 时置为 queued，避免覆盖消费者已经写入的 running 状态。
	return s.db.WithContext(ctx).Model(&PDFImportJob{}).
		Where("job_id = ? AND status = ?", jobID, "pending").
		Updates(map[string]any{
			"status":     "queued",
			"updated_at": gorm.Expr("now()"),
		}).Error
}

// RequeueStalePDFJobs 在服务启动时恢复未完成任务：
// 上次进程中断留下的 running 重置为 pending，并返回需要重新投递的任务。
func (s *Store) RequeueStalePDFJobs(ctx context.Context) ([]string, error) {
	if err := s.db.WithContext(ctx).Model(&PDFImportJob{}).
		Where("status = ? AND deleted_at IS NULL", "running").
		Updates(map[string]any{
			"status":     "pending",
			"updated_at": gorm.Expr("now()"),
		}).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	err := s.db.WithContext(ctx).Model(&PDFImportJob{}).
		Where("status IN ? AND deleted_at IS NULL", []string{"pending", "queued"}).
		Order("created_at ASC").
		Pluck("job_id", &ids).Error
	return ids, err
}

// StartPDFImportJob 标记任务开始执行。
func (s *Store) StartPDFImportJob(ctx context.Context, jobID string) error {
	return s.db.WithContext(ctx).Model(&PDFImportJob{}).Where("job_id = ?", jobID).
		Updates(map[string]any{
			"status":     "running",
			"message":    "",
			"started_at": gorm.Expr("now()"),
			"updated_at": gorm.Expr("now()"),
		}).Error
}

// FinishPDFImportJob 写入解析结果或失败原因。
func (s *Store) FinishPDFImportJob(ctx context.Context, jobID, status, message string, pages int, result json.RawMessage) error {
	return s.db.WithContext(ctx).Model(&PDFImportJob{}).Where("job_id = ?", jobID).
		Updates(map[string]any{
			"status":      status,
			"message":     message,
			"page_count":  pages,
			"result_json": aidExpr(result),
			"finished_at": gorm.Expr("now()"),
			"updated_at":  gorm.Expr("now()"),
		}).Error
}

// UpdatePDFImportResult 更新解析摘要（人工校对后回写）。
func (s *Store) UpdatePDFImportResult(ctx context.Context, jobID string, result json.RawMessage) error {
	return s.db.WithContext(ctx).Model(&PDFImportJob{}).Where("job_id = ?", jobID).
		Updates(map[string]any{
			"result_json": aidExpr(result),
			"updated_at":  gorm.Expr("now()"),
		}).Error
}

// ResetPDFImportJob 把失败任务重置为待解析，供重新投递。
func (s *Store) ResetPDFImportJob(ctx context.Context, jobID string) error {
	return s.db.WithContext(ctx).Model(&PDFImportJob{}).Where("job_id = ?", jobID).
		Updates(map[string]any{
			"status":      "pending",
			"message":     "",
			"page_count":  0,
			"result_json": gorm.Expr("NULL::jsonb"),
			"started_at":  nil,
			"finished_at": nil,
			"updated_at":  gorm.Expr("now()"),
		}).Error
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
func (s *Store) CreateListeningJob(ctx context.Context, jobID, examID, lang, sourceURL string, hasAnswers bool) error {
	job := ListeningJob{
		JobID:      jobID,
		ExamID:     examID,
		Lang:       lang,
		Level:      "A1",
		SourceURL:  sourceURL,
		Status:     "pending",
		HasAnswers: hasAnswers,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "job_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"exam_id":       job.ExamID,
			"lang":          job.Lang,
			"source_url":    job.SourceURL,
			"status":        "pending",
			"has_answers":   hasAnswers,
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

// UpdateListeningJobMeta 更新听力任务名称与等级；name 为 nil 表示清除自定义名称。
func (s *Store) UpdateListeningJobMeta(ctx context.Context, jobID string, name *string, level string) error {
	updates := map[string]any{
		"name":       name,
		"updated_at": gorm.Expr("now()"),
	}
	if level != "" {
		updates["level"] = level
	}
	return s.db.WithContext(ctx).Model(&ListeningJob{}).
		Where("job_id = ? AND deleted_at IS NULL", jobID).
		Updates(updates).Error
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

// ErrListeningExamIDActiveConflict 表示同 exam_id 已存在一条活跃（未删除）记录，
// 由部分唯一索引 uq_listening_jobs_exam_id_active 约束。
var ErrListeningExamIDActiveConflict = errors.New("该 exam_id 已存在未删除的任务")

// ErrListeningJobNotFound 表示指定 job_id 不存在。
var ErrListeningJobNotFound = errors.New("listening job not found")

// GetListeningJob 按 job_id 取单条任务（含已软删除），不存在返回 nil。
func (s *Store) GetListeningJob(ctx context.Context, jobID string) (*ListeningJob, error) {
	var j ListeningJob
	err := s.db.WithContext(ctx).Table("listening_jobs").
		Select(`job_id, exam_id, name, lang, level, source_url, status,
			COALESCE(manifest_path,'') AS manifest_path,
			COALESCE(log_path,'') AS log_path,
			COALESCE(error_msg,'') AS error_msg,
			last_log_at, deleted_at, has_answers, created_at, updated_at`).
		Where("job_id = ?", jobID).
		Take(&j).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &j, nil
}

// CountActiveListeningJobsByExamID 统计指定 exam_id 的活跃（未删除）任务数；
// excludeJobID 非空时排除该 job（用于硬删除前判断产物目录是否还有其他任务引用）。
func (s *Store) CountActiveListeningJobsByExamID(ctx context.Context, examID, excludeJobID string) (int64, error) {
	q := s.db.WithContext(ctx).Table("listening_jobs").
		Where("exam_id = ? AND deleted_at IS NULL", examID)
	if excludeJobID != "" {
		q = q.Where("job_id <> ?", excludeJobID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// SoftDeleteActiveListeningJobsByExamID 软删除指定 exam_id 的全部活跃任务，
// 返回受影响行数。overwrite 上传前调用，保证部分唯一索引不冲突。
func (s *Store) SoftDeleteActiveListeningJobsByExamID(ctx context.Context, examID string) (int64, error) {
	res := s.db.WithContext(ctx).Model(&ListeningJob{}).
		Where("exam_id = ? AND deleted_at IS NULL", examID).
		Updates(map[string]any{
			"deleted_at": gorm.Expr("now()"),
			"updated_at": gorm.Expr("now()"),
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// HardDeleteListeningJob 物理删除任务记录及其日志（产物文件由调用方决定是否删除）。
func (s *Store) HardDeleteListeningJob(ctx context.Context, jobID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM listening_job_logs WHERE job_id = ?", jobID).Error; err != nil {
			return err
		}
		return tx.Exec("DELETE FROM listening_jobs WHERE job_id = ?", jobID).Error
	})
}

// RestoreListeningJob 恢复已软删除的任务。若同 exam_id 已有活跃记录，
// 会违反部分唯一索引，返回 ErrListeningExamIDActiveConflict。
func (s *Store) RestoreListeningJob(ctx context.Context, jobID string) error {
	var examID string
	if err := s.db.WithContext(ctx).Table("listening_jobs").
		Where("job_id = ?", jobID).Pluck("exam_id", &examID).Error; err != nil {
		return err
	}
	if examID == "" {
		return ErrListeningJobNotFound
	}
	active, err := s.CountActiveListeningJobsByExamID(ctx, examID, jobID)
	if err != nil {
		return err
	}
	if active > 0 {
		return ErrListeningExamIDActiveConflict
	}
	res := s.db.WithContext(ctx).Model(&ListeningJob{}).
		Where("job_id = ?", jobID).
		Updates(map[string]any{
			"deleted_at": nil,
			"updated_at": gorm.Expr("now()"),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrListeningJobNotFound
	}
	return nil
}

// --- Listening Job Notifications ---

// InsertNotification 新增一条系统通知；payload 为 nil 时不写入 JSONB 字段。
func (s *Store) InsertNotification(ctx context.Context, jobID, eventType, title, message string, payload map[string]any) (int64, error) {
	var payloadExpr any
	if payload == nil {
		payloadExpr = gorm.Expr("NULL::jsonb")
	} else {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		payloadExpr = gorm.Expr("?::jsonb", string(raw))
	}
	n := &ListeningJobNotification{
		JobID:     jobID,
		EventType: eventType,
		Title:     title,
		Message:   message,
	}
	if err := s.db.WithContext(ctx).Create(&struct {
		ListeningJobNotification
		Payload any `gorm:"column:payload"`
	}{ListeningJobNotification: *n, Payload: payloadExpr}).Error; err != nil {
		return 0, err
	}
	return n.ID, nil
}

// UpsertNotificationByJob 为同一个听力任务维护一条状态通知：
// 存在则原地更新（并重置为未读），不存在则新建。返回通知 id。
// 听力解析是串行任务，每个 job 只会有一条随状态变化的消息，避免消息列表被刷屏。
func (s *Store) UpsertNotificationByJob(ctx context.Context, jobID, eventType, title, message string, payload map[string]any) (int64, error) {
	var id int64
	if err := s.db.WithContext(ctx).Table("listening_job_notifications").
		Where("job_id = ?", jobID).
		Order("id DESC").
		Limit(1).
		Pluck("id", &id).Error; err != nil {
		return 0, err
	}
	if id == 0 {
		return s.InsertNotification(ctx, jobID, eventType, title, message, payload)
	}

	updates := map[string]any{
		"event_type": eventType,
		"title":      title,
		"message":    message,
		"created_at": gorm.Expr("now()"),
		"read_at":    nil,
	}
	if payload == nil {
		updates["payload"] = gorm.Expr("NULL::jsonb")
	} else {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		updates["payload"] = gorm.Expr("?::jsonb", string(raw))
	}
	if err := s.db.WithContext(ctx).Model(&ListeningJobNotification{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return 0, err
	}
	return id, nil
}

// ListNotifications 按创建时间倒序拉取通知；limit 默认 50。
func (s *Store) ListNotifications(ctx context.Context, limit int) ([]*ListeningJobNotification, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []*ListeningJobNotification
	err := s.db.WithContext(ctx).
		Table("listening_job_notifications").
		Order("created_at DESC").
		Limit(limit).
		Scan(&out).Error
	return out, err
}

// CountUnreadNotifications 统计未读通知数。
func (s *Store) CountUnreadNotifications(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).
		Table("listening_job_notifications").
		Where("read_at IS NULL").
		Count(&n).Error
	return n, err
}

// MarkNotificationRead 标记单条通知已读；不存在时返回 false。
func (s *Store) MarkNotificationRead(ctx context.Context, id int64) (bool, error) {
	res := s.db.WithContext(ctx).Exec(
		`UPDATE listening_job_notifications SET read_at = now() WHERE id = ? AND read_at IS NULL`, id,
	)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// MarkAllNotificationsRead 标记全部未读通知为已读。
func (s *Store) MarkAllNotificationsRead(ctx context.Context) (int64, error) {
	res := s.db.WithContext(ctx).Exec(
		`UPDATE listening_job_notifications SET read_at = now() WHERE read_at IS NULL`,
	)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// SetListeningJobHasAnswers 标记该听力素材已绑定答案。
func (s *Store) SetListeningJobHasAnswers(ctx context.Context, jobID string) error {
	return s.db.WithContext(ctx).Model(&ListeningJob{}).
		Where("job_id = ?", jobID).
		Updates(map[string]any{
			"has_answers": true,
			"updated_at":  gorm.Expr("now()"),
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

// ListListeningJobs 返回最近 N 条任务；includeDeleted=false 时排除已软删除的。
func (s *Store) ListListeningJobs(ctx context.Context, limit int, includeDeleted bool) ([]ListeningJob, error) {
	if limit <= 0 {
		limit = 50
	}
	jobs := make([]ListeningJob, 0)
	q := s.db.WithContext(ctx).Table("listening_jobs").
		Select(`job_id, exam_id, name, lang, level, source_url, status,
			COALESCE(manifest_path,'') AS manifest_path,
			COALESCE(log_path,'') AS log_path,
			COALESCE(error_msg,'') AS error_msg,
			last_log_at, deleted_at, has_answers, created_at, updated_at`).
		Order("updated_at DESC").
		Limit(limit)
	if !includeDeleted {
		q = q.Where("deleted_at IS NULL")
	}
	err := q.Find(&jobs).Error
	return jobs, err
}

// ListeningJobDetail 返回单个任务及其日志（含已软删除的，便于管理台查看历史）。
// afterLogID > 0 时只返回该日志 ID 之后的增量日志（升序）；否则返回最近 300 条（降序）。
func (s *Store) ListeningJobDetail(ctx context.Context, jobID string, afterLogID int64) (*ListeningJob, []ListeningJobLog, error) {
	var j ListeningJob
	err := s.db.WithContext(ctx).Table("listening_jobs").
		Select(`job_id, exam_id, name, lang, level, source_url, status,
			COALESCE(manifest_path,'') AS manifest_path,
			COALESCE(log_path,'') AS log_path,
			COALESCE(error_msg,'') AS error_msg,
			last_log_at, deleted_at, has_answers, created_at, updated_at`).
		Where("job_id = ?", jobID).
		Take(&j).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	logs := make([]ListeningJobLog, 0)
	q := s.db.WithContext(ctx).Table("listening_job_logs").
		Select(`log_id, COALESCE(stage,'') AS stage, COALESCE(level,'') AS level, message, created_at`).
		Where("job_id = ?", jobID)
	if afterLogID > 0 {
		q = q.Where("log_id > ?", afterLogID).Order("log_id ASC").Limit(1000)
	} else {
		q = q.Order("log_id DESC").Limit(300)
	}
	if err := q.Find(&logs).Error; err != nil {
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

// FinishBuild 记录构建成功及产物信息（build_versions 由 Go 独占写入）。
func (s *Store) FinishBuild(ctx context.Context, version, dbURL, vectorURL, dbMD5, vectorMD5 string) error {
	return s.db.WithContext(ctx).Model(&Version{}).
		Where("version = ?", version).
		Updates(map[string]any{
			"status":              "built",
			"db_url":              dbURL,
			"vector_snapshot_url": vectorURL,
			"db_md5":              dbMD5,
			"vector_md5":          vectorMD5,
		}).Error
}

// MarkFailed 只把未成功的记录置为 failed，避免覆盖已写入的成功结果。
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
