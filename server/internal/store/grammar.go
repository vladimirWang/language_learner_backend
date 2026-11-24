package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// ListGrammarQuestions 返回语法题，供管理台审核与导出。
func (s *Store) ListGrammarQuestions(ctx context.Context, lang, level, status string, limit int) ([]GrammarQuestion, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	q := s.db.WithContext(ctx).Table("grammar_questions").
		Select("id, lang, level, point_key, point_label, stem, options, answer, explanation, " +
			"source_path, source_sha, generated_by, status, review_note, reviewed_at").
		Where("deleted_at IS NULL")
	if lang != "" {
		q = q.Where("lang = ?", lang)
	}
	if level != "" {
		q = q.Where("level = ?", level)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	out := make([]GrammarQuestion, 0)
	err := q.Order("lang ASC, level ASC, point_key ASC, id ASC").Limit(limit).Scan(&out).Error
	return out, err
}

// UpsertGrammarDrafts 写入 agent 生成的草稿；已通过的题保持 approved 不覆盖状态。
func (s *Store) UpsertGrammarDrafts(ctx context.Context, drafts []GrammarQuestion) (int, error) {
	if len(drafts) == 0 {
		return 0, nil
	}
	written := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, d := range drafts {
			res := tx.Exec(
				`INSERT INTO grammar_questions
				 (lang, level, point_key, point_label, stem, options, answer, explanation,
				  source_path, source_sha, generated_by, status)
				 VALUES (?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, 'draft')
				 ON CONFLICT (lang, stem) DO UPDATE SET
				     level = EXCLUDED.level,
				     point_key = EXCLUDED.point_key,
				     point_label = EXCLUDED.point_label,
				     options = EXCLUDED.options,
				     answer = EXCLUDED.answer,
				     explanation = EXCLUDED.explanation,
				     source_path = EXCLUDED.source_path,
				     source_sha = EXCLUDED.source_sha,
				     generated_by = EXCLUDED.generated_by,
				     status = CASE WHEN grammar_questions.status = 'approved' THEN 'approved' ELSE 'draft' END,
				     review_note = CASE WHEN grammar_questions.status = 'approved' THEN grammar_questions.review_note ELSE '' END,
				     updated_at = now()`,
				d.Lang, d.Level, d.PointKey, d.PointLabel, d.Stem, string(d.Options),
				d.Answer, d.Explanation, d.SourcePath, d.SourceSHA, d.GeneratedBy,
			)
			if res.Error != nil {
				return fmt.Errorf("write grammar draft %q: %w", d.Stem, res.Error)
			}
			written += int(res.RowsAffected)
		}
		return nil
	})
	return written, err
}

// ReviewGrammarQuestions 批量设置审核状态；draft 会清空审核时间。
func (s *Store) ReviewGrammarQuestions(ctx context.Context, ids []int64, status, note string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := s.db.WithContext(ctx).Exec(
		`UPDATE grammar_questions
		 SET status = ?,
		     review_note = ?,
		     reviewed_at = CASE WHEN ? = 'draft' THEN NULL ELSE now() END,
		     updated_at = now()
		 WHERE id IN ? AND deleted_at IS NULL`,
		status, note, status, ids,
	)
	return res.RowsAffected, res.Error
}

// GrammarSource 是 LLM 出题时的单份语法素材，按 source_path 聚合多个 chunk。
type GrammarSource struct {
	Key        string   `json:"key"`   // 由 source_path 归一化的稳定 key
	Label      string   `json:"label"` // 去掉 .md 的文件名
	Level      string   `json:"level"` // 该素材目标等级（可能为空）
	SourcePath string   `json:"source_path"`
	Patterns   []string `json:"patterns"` // 该语法点的结构模式（可选）
	Prompts    []string `json:"prompts"`  // 该语法点的例句（可选）
	Contents   []string `json:"contents"` // chunk.content 片段，LLM 的主要输入
	Hash       string   `json:"hash"`     // 所有片段拼接后的 sha256，用于识别素材是否变更
}

// BatchSoftDeleteGrammarQuestions 批量软删除语法题，返回实际删除条数。
func (s *Store) BatchSoftDeleteGrammarQuestions(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := s.db.WithContext(ctx).Model(&GrammarQuestion{}).
		Where("id IN ? AND deleted_at IS NULL", ids).
		Updates(map[string]any{"deleted_at": gorm.Expr("now()"), "updated_at": gorm.Expr("now()")})
	return res.RowsAffected, res.Error
}

// ListGrammarSources 聚合 rag_chunks 里某语种某等级的语法切片。
// level 为空时返回全部等级；limit 上限保护，单份 source 也只截断内容。
// 如果指定等级过滤后一条都没有，自动降级到该语种全部素材，避免调用方拿到空结果。
func (s *Store) ListGrammarSources(ctx context.Context, lang, level string, limit int) ([]GrammarSource, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	type row struct {
		ChunkID    int64
		Title      string
		Content    string
		SourcePath string
		Level      string
		Patterns   string
		Prompts    string
	}
	load := func(includeLevelFilter bool) ([]row, error) {
		q := s.db.WithContext(ctx).Table("rag_chunks").
			Select("chunk_id, COALESCE(title, '') AS title, COALESCE(content, '') AS content, "+
				"source_path, COALESCE(level, '') AS level, COALESCE(patterns, '') AS patterns, "+
				"COALESCE(prompts, '') AS prompts").
			Where("lang = ? AND chunk_type = 'grammar'", lang)
		if includeLevelFilter && level != "" {
			q = q.Where("(level LIKE ? OR level IS NULL OR level = '')", "%"+level+"%")
		}
		var rows []row
		if err := q.Order("source_path ASC, chunk_id ASC").Limit(limit).Scan(&rows).Error; err != nil {
			return nil, err
		}
		return rows, nil
	}
	rows, err := load(true)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 && level != "" {
		rows, err = load(false)
		if err != nil {
			return nil, err
		}
	}

	grouped := make(map[string]*GrammarSource)
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		if _, ok := grouped[r.SourcePath]; !ok {
			label := strings.TrimSuffix(path.Base(r.SourcePath), ".md")
			order = append(order, r.SourcePath)
			grouped[r.SourcePath] = &GrammarSource{
				Key:        grammarSourceKey(r.SourcePath),
				Label:      label,
				Level:      strings.ToUpper(strings.TrimSpace(r.Level)),
				SourcePath: r.SourcePath,
			}
		}
		gs := grouped[r.SourcePath]
		if gs.Level == "" && r.Level != "" {
			gs.Level = strings.ToUpper(strings.TrimSpace(r.Level))
		}
		for _, p := range splitPatterns(r.Patterns) {
			if !contains(gs.Patterns, p) {
				gs.Patterns = append(gs.Patterns, p)
			}
		}
		for _, p := range splitPrompts(r.Prompts) {
			if !contains(gs.Prompts, p) {
				gs.Prompts = append(gs.Prompts, p)
			}
		}
		// 用标题 + 内容拼接，既给上下文也方便哈希。
		body := strings.TrimSpace(r.Content)
		if body == "" {
			continue
		}
		if utf8RuneCount(body) > 500 {
			body = string([]rune(body)[:500])
		}
		if r.Title != "" {
			body = r.Title + "：" + body
		}
		gs.Contents = append(gs.Contents, body)
	}

	out := make([]GrammarSource, 0, len(order))
	for _, p := range order {
		gs := grouped[p]
		// 至少要有一条内容，纯 patterns/prompts 不算有效素材。
		if len(gs.Contents) == 0 {
			continue
		}
		gs.Hash = grammarSourceHash(gs)
		out = append(out, *gs)
	}
	return out, nil
}

func grammarSourceKey(sourcePath string) string {
	name := strings.TrimSuffix(path.Base(sourcePath), ".md")
	return strings.ToLower(strings.TrimSpace(name))
}

func grammarSourceHash(gs *GrammarSource) string {
	sort.Strings(gs.Contents)
	joined := strings.Join(gs.Contents, "\u0001")
	return fmt.Sprintf("%x", sumSHA256([]byte(joined)))
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func utf8RuneCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func sumSHA256(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}
