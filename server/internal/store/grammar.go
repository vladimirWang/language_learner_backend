package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// ListGrammarQuestions 返回语法题，供管理台审核与导出。
func (s *Store) ListGrammarQuestions(ctx context.Context, lang, level, status string, limit int) ([]GrammarQuestion, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	q := s.db.WithContext(ctx).Table("grammar_questions").
		Select("id, lang, level, point_key, point_label, stem, options, answer, explanation, " +
			"source_path, source_sha, generated_by, status, review_note, reviewed_at")
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
		 WHERE id IN ?`,
		status, note, status, ids,
	)
	return res.RowsAffected, res.Error
}
