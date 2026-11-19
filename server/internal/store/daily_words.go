package store

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"
)

// JSONRaw 让 JSONB 列既能被数据库驱动扫描，也能在空值时序列化为空数组。
type JSONRaw json.RawMessage

// Scan 实现 sql.Scanner。
func (j *JSONRaw) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*j = nil
	case []byte:
		*j = append((*j)[:0], v...)
	case string:
		*j = append((*j)[:0], v...)
	default:
		return fmt.Errorf("unsupported jsonb type %T", value)
	}
	return nil
}

// MarshalJSON 未设置时输出 []，避免客户端拿到 null。
func (j JSONRaw) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("[]"), nil
	}
	return j, nil
}

// VocabWord 是每日新词接口返回的词条。
type VocabWord struct {
	ID          int64   `gorm:"column:id" json:"word_id"`
	Lang        string  `gorm:"column:lang" json:"-"`
	Lemma       string  `gorm:"column:lemma" json:"lemma"`
	Level       string  `gorm:"column:level" json:"-"`
	WordType    *string `gorm:"column:word_type" json:"word_type,omitempty"`
	Gender      *string `gorm:"column:gender" json:"gender,omitempty"`
	Plural      *string `gorm:"column:plural" json:"plural,omitempty"`
	ChineseMean string  `gorm:"column:chinese_mean" json:"chinese_mean"`
	EnglishMean *string `gorm:"column:english_mean" json:"english_mean,omitempty"`
	ExampleDE   *string `gorm:"column:example_de" json:"example_de,omitempty"`
}

// RecentWord 是口语方案详情里附带的新词提示。
type RecentWord struct {
	Lemma       string `gorm:"column:lemma" json:"lemma"`
	ChineseMean string `gorm:"column:chinese_mean" json:"chinese_mean"`
	Level       string `gorm:"column:level" json:"level"`
}

// GrammarPoint 是可选语法点清单中的一项，按源文档聚合。
type GrammarPoint struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	Level      string   `json:"level"`
	SourcePath string   `json:"source_path"`
	Patterns   []string `json:"patterns"`
}

// GrammarRequirement 是方案级语法要求；整场任意回答达成即可。
type GrammarRequirement struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	SourcePath string   `json:"source_path"`
	Patterns   []string `json:"patterns"`
	MinUses    int      `json:"min_uses"`
	Enforce    string   `json:"enforce"`
	HintParts  []string `json:"hint_parts"`
}

const (
	maxDailyWords = 20
	dailyWordsTZ  = "2006-01-02"
)

// DailyWords 返回某天给某设备的词；同一天同一设备结果稳定，不写学习记录。
func (s *Store) DailyWords(ctx context.Context, lang, level string, count int, deviceID string, day time.Time) ([]VocabWord, error) {
	if count <= 0 {
		count = 5
	}
	if count > maxDailyWords {
		count = maxDailyWords
	}
	dayStr := day.Format(dailyWordsTZ)

	ids, err := s.loadDailyAssignment(ctx, lang, level, dayStr, deviceID)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		words, complete, err := s.wordsByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		if complete {
			return words, nil
		}
	}

	ids, err = s.pickDailyWordIDs(ctx, lang, level, dayStr, deviceID, count)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []VocabWord{}, nil
	}
	if err := s.saveDailyAssignment(ctx, lang, level, dayStr, deviceID, ids); err != nil {
		return nil, err
	}
	words, _, err := s.wordsByIDs(ctx, ids)
	return words, err
}

func (s *Store) loadDailyAssignment(ctx context.Context, lang, level, dayStr, deviceID string) ([]int64, error) {
	var row struct {
		WordIDs string `gorm:"column:word_ids"`
	}
	err := s.db.WithContext(ctx).
		Table("daily_word_assignments").
		Select("word_ids::text AS word_ids").
		Where("lang = ? AND level = ? AND day = ?::date AND device_id = ?", lang, level, dayStr, deviceID).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return parseInt64Array(row.WordIDs), nil
}

func (s *Store) pickDailyWordIDs(ctx context.Context, lang, level, dayStr, deviceID string, count int) ([]int64, error) {
	sql := "SELECT id FROM vocab_words WHERE lang = ?"
	args := []any{lang}
	if level != "" {
		sql += " AND level = ?"
		args = append(args, level)
	}
	// 把 device_id 计入哈希：不同设备同一天拿到不同词表，公共分配（空串）仍稳定可复现。
	sql += " ORDER BY md5(lemma || ? || ?) ASC, id ASC LIMIT ?"
	args = append(args, dayStr, deviceID, count)

	ids := make([]int64, 0, count)
	err := s.db.WithContext(ctx).Raw(sql, args...).Scan(&ids).Error
	return ids, err
}

func (s *Store) saveDailyAssignment(ctx context.Context, lang, level, dayStr, deviceID string, ids []int64) error {
	return s.db.WithContext(ctx).Exec(
		`INSERT INTO daily_word_assignments (lang, level, day, device_id, word_ids)
		 VALUES (?, ?, ?::date, ?, ?::bigint[])
		 ON CONFLICT (lang, level, day, device_id)
		 DO UPDATE SET word_ids = EXCLUDED.word_ids`,
		lang, level, dayStr, deviceID, formatInt64Array(ids),
	).Error
}

// wordsByIDs 按 ids 顺序返回词条；complete 为 false 表示有词条已不存在。
func (s *Store) wordsByIDs(ctx context.Context, ids []int64) ([]VocabWord, bool, error) {
	if len(ids) == 0 {
		return []VocabWord{}, true, nil
	}
	found := make([]VocabWord, 0, len(ids))
	if err := s.db.WithContext(ctx).Table("vocab_words").Where("id IN ?", ids).Scan(&found).Error; err != nil {
		return nil, false, err
	}
	byID := make(map[int64]VocabWord, len(found))
	for _, word := range found {
		byID[word.ID] = word
	}
	ordered := make([]VocabWord, 0, len(ids))
	for _, id := range ids {
		word, ok := byID[id]
		if !ok {
			continue
		}
		ordered = append(ordered, word)
	}
	return ordered, len(ordered) == len(ids), nil
}

// MarkWordsLearned 记录设备已学词；未知 ID 忽略，返回 (accepted, ignored)。
func (s *Store) MarkWordsLearned(ctx context.Context, deviceID string, ids []int64) (int, int, error) {
	if deviceID == "" {
		return 0, len(ids), nil
	}
	existing := make([]int64, 0, len(ids))
	if err := s.db.WithContext(ctx).Table("vocab_words").Where("id IN ?", ids).Pluck("id", &existing).Error; err != nil {
		return 0, 0, err
	}
	for _, id := range existing {
		if err := s.db.WithContext(ctx).Exec(
			`INSERT INTO user_word_learning (device_id, word_id, learned_at)
			 VALUES (?, ?, now())
			 ON CONFLICT (device_id, word_id) DO UPDATE SET learned_at = now()`,
			deviceID, id,
		).Error; err != nil {
			return 0, 0, err
		}
	}
	return len(existing), len(ids) - len(existing), nil
}

// RecentWords 返回设备最近五天内学过的词，按学习时间倒序、lemma 去重，最多 20 条。
func (s *Store) RecentWords(ctx context.Context, deviceID, lang string, since time.Time) ([]RecentWord, error) {
	out := make([]RecentWord, 0)
	if deviceID == "" {
		return out, nil
	}
	err := s.db.WithContext(ctx).Raw(
		`SELECT lemma, chinese_mean, level FROM (
		     SELECT DISTINCT ON (vw.lemma) vw.lemma, vw.chinese_mean, vw.level, l.learned_at
		     FROM user_word_learning l
		     JOIN vocab_words vw ON vw.id = l.word_id
		     WHERE l.device_id = ? AND l.learned_at >= ? AND vw.lang = ?
		     ORDER BY vw.lemma, l.learned_at DESC
		 ) t
		 ORDER BY learned_at DESC
		 LIMIT 20`,
		deviceID, since, lang,
	).Scan(&out).Error
	return out, err
}

// ListGrammarPoints 按源文档聚合语法切片，生成可选语法点。
func (s *Store) ListGrammarPoints(ctx context.Context, lang, level string) ([]GrammarPoint, error) {
	type row struct {
		SourcePath string
		Level      string
		Patterns   string
	}
	rows := make([]row, 0, 64)
	tx := s.db.WithContext(ctx).Table("rag_chunks").
		Select("source_path, COALESCE(level, '') AS level, COALESCE(patterns, '') AS patterns").
		Where("lang = ? AND chunk_type = 'grammar'", lang)
	if level != "" {
		tx = tx.Where("(level LIKE ? OR level IS NULL OR level = '')", "%"+level+"%")
	}
	if err := tx.Order("source_path ASC, chunk_id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}

	points := make([]GrammarPoint, 0, len(rows))
	index := make(map[string]int, len(rows))
	seenPatterns := make(map[string]map[string]bool, len(rows))
	for _, r := range rows {
		key := grammarPointKey(r.SourcePath)
		pos, ok := index[key]
		if !ok {
			pos = len(points)
			index[key] = pos
			seenPatterns[key] = make(map[string]bool)
			points = append(points, GrammarPoint{
				Key:        key,
				Label:      strings.TrimSuffix(path.Base(r.SourcePath), ".md"),
				Level:      r.Level,
				SourcePath: r.SourcePath,
				Patterns:   []string{},
			})
		}
		if points[pos].Level == "" && r.Level != "" {
			points[pos].Level = r.Level
		}
		for _, pattern := range splitPatterns(r.Patterns) {
			if seenPatterns[key][pattern] {
				continue
			}
			seenPatterns[key][pattern] = true
			points[pos].Patterns = append(points[pos].Patterns, pattern)
		}
	}
	return points, nil
}

// GrammarSourcePathsMissing 返回 sourcePaths 中不存在于当前语种 grammar 切片的文档路径。
func (s *Store) GrammarSourcePathsMissing(ctx context.Context, lang string, sourcePaths []string) ([]string, error) {
	if len(sourcePaths) == 0 {
		return nil, nil
	}
	found := make([]string, 0, len(sourcePaths))
	if err := s.db.WithContext(ctx).Raw(
		`SELECT DISTINCT source_path FROM rag_chunks
		 WHERE lang = ? AND chunk_type = 'grammar' AND source_path IN ?`,
		lang, sourcePaths,
	).Scan(&found).Error; err != nil {
		return nil, err
	}
	exists := make(map[string]bool, len(found))
	for _, item := range found {
		exists[item] = true
	}
	missing := make([]string, 0, len(sourcePaths))
	for _, item := range sourcePaths {
		if !exists[item] {
			missing = append(missing, item)
		}
	}
	return missing, nil
}

// GetGrammarRequirements 读取方案级语法要求；方案不存在时返回 ErrNotFound。
func (s *Store) GetGrammarRequirements(ctx context.Context, planID int64) ([]GrammarRequirement, error) {
	var row struct {
		Raw JSONRaw `gorm:"column:grammar_requirements"`
	}
	err := s.db.WithContext(ctx).
		Table("speaking_exam_plans").
		Select("grammar_requirements").
		Where("id = ?", planID).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	requirements := make([]GrammarRequirement, 0)
	if len(row.Raw) == 0 {
		return requirements, nil
	}
	if err := json.Unmarshal(row.Raw, &requirements); err != nil {
		return nil, err
	}
	return requirements, nil
}

func grammarPointKey(sourcePath string) string {
	base := strings.ToLower(strings.TrimSuffix(path.Base(sourcePath), ".md"))
	var b strings.Builder
	for _, r := range base {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	key := strings.Trim(b.String(), "-")
	if key == "" {
		sum := md5.Sum([]byte(sourcePath))
		key = fmt.Sprintf("point-%x", sum)[:14]
	}
	return key
}

func splitPatterns(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if pattern := strings.TrimSpace(part); pattern != "" {
			out = append(out, pattern)
		}
	}
	return out
}

func parseInt64Array(raw string) []int64 {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "{")
	raw = strings.TrimSuffix(raw, "}")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func formatInt64Array(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
