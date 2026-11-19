package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// VocabWordRecord 是管理台新词库列表的一行，字段与 vocab_words 对齐。
type VocabWordRecord struct {
	ID           int64     `gorm:"column:id" json:"word_id"`
	Lang         string    `gorm:"column:lang" json:"lang"`
	Lemma        string    `gorm:"column:lemma" json:"lemma"`
	Level        string    `gorm:"column:level" json:"level"`
	WordType     *string   `gorm:"column:word_type" json:"word_type,omitempty"`
	Gender       *string   `gorm:"column:gender" json:"gender,omitempty"`
	Plural       *string   `gorm:"column:plural" json:"plural,omitempty"`
	ChineseMean  string    `gorm:"column:chinese_mean" json:"chinese_mean"`
	EnglishMean  *string   `gorm:"column:english_mean" json:"english_mean,omitempty"`
	ExampleDE    *string   `gorm:"column:example_de" json:"example_de,omitempty"`
	SourceMDPath *string   `gorm:"column:source_md_path" json:"source_md_path,omitempty"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updated_at"`
}

// VocabWordInput 是新增/更新词条的规范化字段；空字符串写入库时转为 NULL。
type VocabWordInput struct {
	Lang         string
	Lemma        string
	Level        string
	WordType     string
	Gender       string
	Plural       string
	ChineseMean  string
	EnglishMean  string
	ExampleDE    string
	SourceMDPath string
}

const vocabWordColumns = `id, lang, lemma, level, word_type, gender, plural,
	chinese_mean, english_mean, example_de, source_md_path, updated_at`

// vocabWordQuery 构造列表查询的公共条件，每次调用返回独立链，避免 Count 污染后续 Select。
func (s *Store) vocabWordQuery(ctx context.Context, lang, level, query string) *gorm.DB {
	tx := s.db.WithContext(ctx).Table("vocab_words")
	if lang != "" {
		tx = tx.Where("lang = ?", lang)
	}
	if level != "" {
		tx = tx.Where("level = ?", level)
	}
	if query != "" {
		like := "%" + query + "%"
		tx = tx.Where(
			"(lemma ILIKE ? OR chinese_mean ILIKE ? OR COALESCE(english_mean, '') ILIKE ?)",
			like, like, like,
		)
	}
	return tx
}

// ListVocabWords 按条件分页返回词条，同时返回符合条件的总数。
func (s *Store) ListVocabWords(ctx context.Context, lang, level, query string, limit, offset int) ([]VocabWordRecord, int64, error) {
	var total int64
	if err := s.vocabWordQuery(ctx, lang, level, query).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]VocabWordRecord, 0, limit)
	err := s.vocabWordQuery(ctx, lang, level, query).
		Select(vocabWordColumns).
		Order("lemma ASC, id ASC").
		Limit(limit).
		Offset(offset).
		Scan(&rows).Error
	return rows, total, err
}

const upsertVocabWordSQL = `
	INSERT INTO vocab_words
		(lang, lemma, level, word_type, gender, plural, chinese_mean, english_mean,
		 example_de, source_md_path)
	VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))
	ON CONFLICT (lang, lemma) DO UPDATE SET
		level = EXCLUDED.level,
		word_type = EXCLUDED.word_type,
		gender = EXCLUDED.gender,
		plural = EXCLUDED.plural,
		chinese_mean = EXCLUDED.chinese_mean,
		english_mean = EXCLUDED.english_mean,
		example_de = EXCLUDED.example_de,
		source_md_path = EXCLUDED.source_md_path,
		updated_at = now()
	RETURNING id, (xmax = 0) AS inserted`

func vocabWordArgs(in VocabWordInput) []any {
	return []any{
		in.Lang, in.Lemma, in.Level, in.WordType, in.Gender, in.Plural,
		in.ChineseMean, in.EnglishMean, in.ExampleDE, in.SourceMDPath,
	}
}

type vocabUpsertResult struct {
	ID       int64 `gorm:"column:id"`
	Inserted bool  `gorm:"column:inserted"`
}

// CreateVocabWord 新增词条；若 (lang, lemma) 已存在返回 created=false 与已有 ID。
func (s *Store) CreateVocabWord(ctx context.Context, in VocabWordInput) (int64, bool, error) {
	var out struct {
		ID int64 `gorm:"column:id"`
	}
	err := s.db.WithContext(ctx).Raw(`
		INSERT INTO vocab_words
			(lang, lemma, level, word_type, gender, plural, chinese_mean, english_mean,
			 example_de, source_md_path)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))
		ON CONFLICT (lang, lemma) DO NOTHING
		RETURNING id`,
		vocabWordArgs(in)...,
	).Scan(&out).Error
	if err != nil {
		return 0, false, err
	}
	if out.ID == 0 {
		return 0, false, nil
	}
	return out.ID, true, nil
}

// UpdateVocabWord 按 ID 更新词条；返回是否命中记录。
func (s *Store) UpdateVocabWord(ctx context.Context, id int64, in VocabWordInput) (bool, error) {
	res := s.db.WithContext(ctx).Exec(`
		UPDATE vocab_words SET
			lemma = ?, level = ?, word_type = NULLIF(?, ''), gender = NULLIF(?, ''),
			plural = NULLIF(?, ''), chinese_mean = ?, english_mean = NULLIF(?, ''),
			example_de = NULLIF(?, ''), source_md_path = NULLIF(?, ''), updated_at = now()
		WHERE id = ?`,
		in.Lemma, in.Level, in.WordType, in.Gender, in.Plural, in.ChineseMean,
		in.EnglishMean, in.ExampleDE, in.SourceMDPath, id,
	)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteVocabWord 按 ID 删除词条；学习记录由外键级联清理。
func (s *Store) DeleteVocabWord(ctx context.Context, id int64) (bool, error) {
	res := s.db.WithContext(ctx).Exec("DELETE FROM vocab_words WHERE id = ?", id)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpsertVocabWords 在单个事务内批量 upsert，供 CSV 导入使用；返回新增与更新数量。
func (s *Store) UpsertVocabWords(ctx context.Context, rows []VocabWordInput) (int, int, error) {
	inserted, updated := 0, 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			var out vocabUpsertResult
			if err := tx.Raw(upsertVocabWordSQL, vocabWordArgs(row)...).Scan(&out).Error; err != nil {
				return err
			}
			if out.Inserted {
				inserted++
			} else {
				updated++
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return inserted, updated, nil
}
