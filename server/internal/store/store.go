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
	DeletedAt    *time.Time `gorm:"column:deleted_at" json:"-"`
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
	Difficulty  string    `gorm:"column:difficulty" json:"difficulty"`
	Active      bool      `gorm:"column:active" json:"active"`
	Description string    `gorm:"column:description" json:"description"`
	SortOrder   int       `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Level) TableName() string { return "levels" }

// LevelExam 对应 level_exams 一行：关卡与题目（听力 exam / 口语题目）的关联。
type LevelExam struct {
	ID        int64     `gorm:"primaryKey;column:id" json:"-"`
	LevelID   int64     `gorm:"column:level_id" json:"level_id"`
	ExamID    string    `gorm:"column:exam_id" json:"exam_id"`
	ItemType  string    `gorm:"column:item_type" json:"item_type"`
	SortOrder int       `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (LevelExam) TableName() string { return "level_exams" }

// SpeakingItem 对应 speaking_items 一行：口语固定问答题库。
type SpeakingItem struct {
	ItemKey        string    `gorm:"primaryKey;column:item_key" json:"item_key"`
	Lang           string    `gorm:"column:lang" json:"lang"`
	Level          string    `gorm:"column:level" json:"level"`
	ExamPart       string    `gorm:"column:exam_part" json:"exam_part"`
	Topic          string    `gorm:"column:topic" json:"topic"`
	Role           string    `gorm:"column:role" json:"role"`
	PromptDE       string    `gorm:"column:prompt_de" json:"prompt_de"`
	PromptZH       string    `gorm:"column:prompt_zh" json:"prompt_zh"`
	ExpectedPoints string    `gorm:"column:expected_points" json:"expected_points"`
	RefAnswer      string    `gorm:"column:ref_answer" json:"ref_answer"`
	ErrorNote      string    `gorm:"column:error_note" json:"error_note"`
	SortOrder      int       `gorm:"column:sort_order" json:"sort_order"`
	Active         bool      `gorm:"column:active" json:"active"`
	CreatedAt      time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at" json:"updated_at"`
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
			if err := tx.Table("level_exams").Where("level_id = ?", id).Count(&cnt).Error; err != nil {
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
			if err := tx.Table("level_exams").Where("level_id = ?", id).Count(&cnt).Error; err != nil {
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

// ListLevelExams 返回关卡关联的内容，按 sort_order 排序。
// 口语以话题为单位返回（exam_id 即话题名），由客户端进入主题对话页。
// 已软删除的听力 exam 不返回。
func (s *Store) ListLevelExams(ctx context.Context, levelID int64) ([]LevelExamInfo, error) {
	rows := make([]LevelExamInfo, 0)
	// 听力 exam 与历史遗留的单题口语关联。
	if err := s.db.WithContext(ctx).
		Table("level_exams le").
		Select(`le.exam_id, le.item_type, le.sort_order, le.created_at,
			COALESCE(j.lang, si.lang, '') AS lang,
			COALESCE(j.source_url, '') AS source_url,
			COALESCE(NULLIF(j.name, ''), si.prompt_de, '') AS name`).
		Joins("LEFT JOIN listening_jobs j ON le.item_type = 'listening' AND j.exam_id = le.exam_id AND j.deleted_at IS NULL").
		Joins("LEFT JOIN speaking_items si ON le.item_type = 'speaking' AND si.item_key = le.exam_id").
		Where("le.level_id = ?", levelID).
		Where("(le.item_type = 'listening' AND j.exam_id IS NOT NULL) OR (le.item_type = 'speaking' AND si.item_key IS NOT NULL)").
		Order("le.sort_order ASC, le.id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	topics := make([]LevelExamInfo, 0)
	err := s.db.WithContext(ctx).
		Table("level_exams le").
		Select(`le.exam_id, le.item_type, le.sort_order, le.created_at,
			COALESCE(l.lang, '') AS lang, '' AS source_url, le.exam_id AS name`).
		Joins("LEFT JOIN levels l ON l.id = le.level_id").
		Where("le.level_id = ? AND le.item_type = 'speaking_topic'", levelID).
		Order("le.sort_order ASC, le.id ASC").
		Scan(&topics).Error
	if err != nil {
		return nil, err
	}
	rows = append(rows, topics...)
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

// ListAvailableExams 返回可加入指定关卡且尚未关联的听力 exam 与口语话题。
// 口语以话题为单位：只返回该关卡语种与难度下、已填写要点的话题。
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
		      SELECT exam_id FROM level_exams WHERE level_id = ? AND item_type = 'listening'
		  )
		UNION ALL
		SELECT 'speaking_topic' AS item_type, si.topic AS exam_id,
		       si.topic || '（' || COUNT(*)::text || ' 题）' AS name,
		       si.lang, '' AS source_url, MIN(si.created_at) AS created_at
		FROM speaking_items si
		WHERE si.active = true AND COALESCE(si.expected_points, '') <> ''
		  AND si.lang = ? AND si.level = ?
		  AND si.topic NOT IN (
		      SELECT exam_id FROM level_exams WHERE level_id = ? AND item_type = 'speaking_topic'
		  )
		GROUP BY si.topic, si.lang
		ORDER BY item_type ASC, created_at DESC
	`, levelID, lv.Lang, lv.Difficulty, levelID).Scan(&rows).Error
	return rows, err
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

// ListListeningJobs 返回最近 N 条任务（已软删除的除外）。
func (s *Store) ListListeningJobs(ctx context.Context, limit int) ([]ListeningJob, error) {
	if limit <= 0 {
		limit = 50
	}
	jobs := make([]ListeningJob, 0)
	err := s.db.WithContext(ctx).Table("listening_jobs").
		Select(`job_id, exam_id, name, lang, level, source_url, status,
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

// ListeningJobDetail 返回单个任务及其日志（已软删除的返回 not found）。
// afterLogID > 0 时只返回该日志 ID 之后的增量日志（升序）；否则返回最近 300 条（降序）。
func (s *Store) ListeningJobDetail(ctx context.Context, jobID string, afterLogID int64) (*ListeningJob, []ListeningJobLog, error) {
	var j ListeningJob
	err := s.db.WithContext(ctx).Table("listening_jobs").
		Select(`job_id, exam_id, name, lang, level, source_url, status,
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
