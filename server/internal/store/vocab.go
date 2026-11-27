package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 单词表按语种拆分：各语种字段差异较大，独立建表；三张表共用
// vocab_words_id_seq，word_id 全局唯一，学习记录无需记录语种。
const (
	vocabTableDE = "vocab_words_de"
	vocabTableEN = "vocab_words_en"
	vocabTableJA = "vocab_words_ja"
)

var vocabTables = []string{vocabTableDE, vocabTableEN, vocabTableJA}

// ErrUnsupportedVocabLang 表示语种没有对应的词表。
var ErrUnsupportedVocabLang = errors.New("unsupported vocab lang")

// vocabTableByLang 返回语种对应的词表名；未知语种返回空串。
func vocabTableByLang(lang string) string {
	switch lang {
	case "de":
		return vocabTableDE
	case "en":
		return vocabTableEN
	case "ja":
		return vocabTableJA
	}
	return ""
}

// vocabExampleColumn 返回该语种例句所在的列名。
func vocabExampleColumn(lang string) string {
	switch lang {
	case "de":
		return "example_de"
	case "en":
		return "example_en"
	case "ja":
		return "example_ja"
	}
	return ""
}

// VocabWordRecord 是管理台新词库列表的一行；各语种专有字段在其它语种下为 null。
type VocabWordRecord struct {
	ID           int64     `gorm:"column:id" json:"word_id"`
	Lang         string    `gorm:"column:lang" json:"lang"`
	Lemma        string    `gorm:"column:lemma" json:"lemma"`
	Level        string    `gorm:"column:level" json:"level"`
	WordType     *string   `gorm:"column:word_type" json:"word_type,omitempty"`
	Gender       *string   `gorm:"column:gender" json:"gender,omitempty"`
	Plural       *string   `gorm:"column:plural" json:"plural,omitempty"`
	Phonetic     *string   `gorm:"column:phonetic" json:"phonetic,omitempty"`
	Kana         *string   `gorm:"column:kana" json:"kana,omitempty"`
	Kanji        *string   `gorm:"column:kanji" json:"kanji,omitempty"`
	Romaji       *string   `gorm:"column:romaji" json:"romaji,omitempty"`
	ChineseMean  string    `gorm:"column:chinese_mean" json:"chinese_mean"`
	EnglishMean  *string   `gorm:"column:english_mean" json:"english_mean,omitempty"`
	ExampleDE    *string   `gorm:"column:example_de" json:"example_de,omitempty"`
	ExampleEN    *string   `gorm:"column:example_en" json:"example_en,omitempty"`
	ExampleJA    *string   `gorm:"column:example_ja" json:"example_ja,omitempty"`
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
	Phonetic     string
	Kana         string
	Kanji        string
	Romaji       string
	Separable    string
	TransIntr    string
	CoreCase     string
	SortKey      string
	ExtraJSON    string
	ChineseMean  string
	EnglishMean  string
	ExampleDE    string
	ExampleEN    string
	ExampleJA    string
	SourceMDPath string
}

// vocabSelectColumns 把某语种表补齐成统一列集，供列表查询与 union 使用。
func vocabSelectColumns(table string) string {
	switch table {
	case vocabTableDE:
		return `id, lemma, level, word_type, gender, plural,
			NULL::text AS phonetic, NULL::text AS kana, NULL::text AS kanji, NULL::text AS romaji,
			chinese_mean, english_mean, example_de,
			NULL::text AS example_en, NULL::text AS example_ja, source_md_path, updated_at`
	case vocabTableEN:
		return `id, lemma, level, word_type, NULL::text AS gender, NULL::text AS plural,
			phonetic, NULL::text AS kana, NULL::text AS kanji, NULL::text AS romaji,
			chinese_mean, english_mean, NULL::text AS example_de, example_en,
			NULL::text AS example_ja, source_md_path, updated_at`
	case vocabTableJA:
		return `id, lemma, level, word_type, NULL::text AS gender, NULL::text AS plural,
			NULL::text AS phonetic, kana, kanji, romaji,
			chinese_mean, english_mean, NULL::text AS example_de,
			NULL::text AS example_en, example_ja, source_md_path, updated_at`
	}
	return ""
}

func vocabSelectSource(lang string) (string, error) {
	if lang != "" {
		table := vocabTableByLang(lang)
		if table == "" {
			return "", ErrUnsupportedVocabLang
		}
		return fmt.Sprintf("SELECT '%s' AS lang, %s FROM %s", lang, vocabSelectColumns(table), table), nil
	}
	parts := make([]string, 0, len(vocabTables))
	for _, table := range vocabTables {
		tableLang := strings.TrimPrefix(table, "vocab_words_")
		parts = append(parts, fmt.Sprintf(
			"SELECT '%s' AS lang, %s FROM %s", tableLang, vocabSelectColumns(table), table,
		))
	}
	return strings.Join(parts, " UNION ALL "), nil
}

// vocabFilter 构造列表查询的公共条件（作用于 union 结果集）。
func vocabFilter(level, query string) (string, []any) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 4)
	if level != "" {
		clauses = append(clauses, "level = ?")
		args = append(args, level)
	}
	if query != "" {
		like := "%" + query + "%"
		clauses = append(clauses,
			`(lemma ILIKE ? OR chinese_mean ILIKE ? OR COALESCE(english_mean, '') ILIKE ?
			  OR COALESCE(kana, '') ILIKE ? OR COALESCE(kanji, '') ILIKE ?)`)
		args = append(args, like, like, like, like, like)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// ListVocabWords 按条件分页返回词条，同时返回符合条件的总数。
// lang 为空时合并三张语种表返回。
func (s *Store) ListVocabWords(ctx context.Context, lang, level, query string, limit, offset int) ([]VocabWordRecord, int64, error) {
	source, err := vocabSelectSource(lang)
	if err != nil {
		return nil, 0, err
	}
	where, filterArgs := vocabFilter(level, query)

	var total int64
	if err := s.db.WithContext(ctx).Raw(
		"SELECT count(*) FROM ("+source+") v"+where, filterArgs...,
	).Scan(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]VocabWordRecord, 0, limit)
	args := append(append([]any{}, filterArgs...), limit, offset)
	err = s.db.WithContext(ctx).Raw(
		"SELECT v.* FROM ("+source+") v"+where+" ORDER BY lemma ASC, id ASC LIMIT ? OFFSET ?",
		args...,
	).Scan(&rows).Error
	return rows, total, err
}

// vocabWriteColumns 返回该语种表中管理台表单可写的列（不含 id 与 updated_at）。
// CSV 批量导入改用 vocabImportColumns，避免管理台编辑把语料字段清空。
func vocabWriteColumns(lang string) []string {
	switch lang {
	case "de":
		return []string{
			"lemma", "level", "word_type", "gender", "plural",
			"chinese_mean", "english_mean", "example_de", "source_md_path",
		}
	case "en":
		return []string{
			"lemma", "level", "word_type", "phonetic",
			"chinese_mean", "english_mean", "example_en", "source_md_path",
		}
	case "ja":
		return []string{
			"lemma", "level", "word_type", "kana", "kanji", "romaji",
			"chinese_mean", "english_mean", "example_ja", "source_md_path",
		}
	}
	return nil
}

// vocabImportColumns 返回 CSV / builder 批量导入可写的全部列，
// 比管理台表单多出只在语料里维护的字段（可分动词、支配格、排序键、extra_json），
// 保证数据库能无损承接 content/<lang>/vocab.csv。
func vocabImportColumns(lang string) []string {
	switch lang {
	case "de":
		return []string{
			"lemma", "level", "word_type", "gender", "plural", "separable",
			"trans_intr", "core_case", "sort_key", "chinese_mean", "english_mean",
			"example_de", "source_md_path", "extra_json",
		}
	case "en":
		return []string{
			"lemma", "level", "word_type", "phonetic", "trans_intr", "core_case",
			"sort_key", "chinese_mean", "english_mean", "example_en",
			"source_md_path", "extra_json",
		}
	case "ja":
		return []string{
			"lemma", "level", "word_type", "kana", "kanji", "romaji", "sort_key",
			"chinese_mean", "english_mean", "example_ja", "source_md_path", "extra_json",
		}
	}
	return nil
}

// vocabColumnValue 返回列对应的值与是否可空。
func vocabColumnValue(in VocabWordInput, column string) (string, bool) {
	switch column {
	case "lemma":
		return in.Lemma, false
	case "level":
		return in.Level, false
	case "word_type":
		return in.WordType, true
	case "gender":
		return in.Gender, true
	case "plural":
		return in.Plural, true
	case "phonetic":
		return in.Phonetic, true
	case "kana":
		return in.Kana, true
	case "kanji":
		return in.Kanji, true
	case "romaji":
		return in.Romaji, true
	case "separable":
		return in.Separable, true
	case "trans_intr":
		return in.TransIntr, true
	case "core_case":
		return in.CoreCase, true
	case "sort_key":
		return in.SortKey, true
	case "extra_json":
		return in.ExtraJSON, true
	case "chinese_mean":
		return in.ChineseMean, false
	case "english_mean":
		return in.EnglishMean, true
	case "example_de":
		return in.ExampleDE, true
	case "example_en":
		return in.ExampleEN, true
	case "example_ja":
		return in.ExampleJA, true
	case "source_md_path":
		return in.SourceMDPath, true
	}
	return "", true
}

// vocabWriteArgs 按列顺序返回参数；可空列传 NULLIF(?, ”)，
// 布尔/JSONB 列额外做类型转换，空串仍写入 NULL。
func vocabWriteArgs(in VocabWordInput, columns []string) ([]string, []any) {
	exprs := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		value, nullable := vocabColumnValue(in, column)
		switch {
		case column == "separable":
			exprs = append(exprs, "NULLIF(?, '')::boolean")
		case column == "extra_json":
			exprs = append(exprs, "NULLIF(?, '')::jsonb")
		case nullable:
			exprs = append(exprs, "NULLIF(?, '')")
		default:
			exprs = append(exprs, "?")
		}
		args = append(args, value)
	}
	return exprs, args
}

func (s *Store) CreateVocabWord(ctx context.Context, in VocabWordInput) (int64, bool, error) {
	table := vocabTableByLang(in.Lang)
	columns := vocabWriteColumns(in.Lang)
	if table == "" || len(columns) == 0 {
		return 0, false, ErrUnsupportedVocabLang
	}
	exprs, args := vocabWriteArgs(in, columns)
	var out struct {
		ID int64 `gorm:"column:id"`
	}
	err := s.db.WithContext(ctx).Raw(fmt.Sprintf(
		`INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (lemma) DO NOTHING RETURNING id`,
		table, strings.Join(columns, ", "), strings.Join(exprs, ", "),
	), args...).Scan(&out).Error
	if err != nil {
		return 0, false, err
	}
	if out.ID == 0 {
		return 0, false, nil
	}
	return out.ID, true, nil
}

// UpdateVocabWord 按语种表与 ID 更新词条；返回是否命中记录。
func (s *Store) UpdateVocabWord(ctx context.Context, id int64, in VocabWordInput) (bool, error) {
	table := vocabTableByLang(in.Lang)
	columns := vocabWriteColumns(in.Lang)
	if table == "" || len(columns) == 0 {
		return false, ErrUnsupportedVocabLang
	}
	exprs, args := vocabWriteArgs(in, columns)
	sets := make([]string, 0, len(columns)+1)
	for i, column := range columns {
		sets = append(sets, column+" = "+exprs[i])
	}
	sets = append(sets, "updated_at = now()")
	res := s.db.WithContext(ctx).Exec(fmt.Sprintf(
		`UPDATE %s SET %s WHERE id = ?`, table, strings.Join(sets, ", "),
	), append(args, id)...)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteVocabWord 删除词条并清理学习记录；lang 为空时依次尝试三张语种表。
func (s *Store) DeleteVocabWord(ctx context.Context, lang string, id int64) (bool, error) {
	tables := vocabTables
	if lang != "" {
		table := vocabTableByLang(lang)
		if table == "" {
			return false, ErrUnsupportedVocabLang
		}
		tables = []string{table}
	}
	deleted := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, table := range tables {
			var found int64
			if err := tx.Raw(
				fmt.Sprintf("SELECT id FROM %s WHERE id = ? FOR UPDATE", table), id,
			).Scan(&found).Error; err != nil {
				return err
			}
			if found == 0 {
				continue
			}
			if err := tx.Exec("DELETE FROM user_word_learning WHERE word_id = ?", id).Error; err != nil {
				return err
			}
			if err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE id = ?", table), id).Error; err != nil {
				return err
			}
			deleted = true
		}
		return nil
	})
	return deleted, err
}

// VocabWordIDsByLemmas 按 lemma 列表精确查找 word_id，顺序与输入一致，
// 未命中的 lemma 自动跳过。供 CSV 导入后反查 word_id 用。
func (s *Store) VocabWordIDsByLemmas(ctx context.Context, lang string, lemmas []string) ([]int64, error) {
	if len(lemmas) == 0 {
		return nil, nil
	}
	table := vocabTableByLang(lang)
	if table == "" {
		return nil, ErrUnsupportedVocabLang
	}
	var rows []struct {
		ID    int64  `gorm:"column:id"`
		Lemma string `gorm:"column:lemma"`
	}
	if err := s.db.WithContext(ctx).Raw(
		fmt.Sprintf("SELECT id, lemma FROM %s WHERE lemma IN ?", table), lemmas,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	byLemma := make(map[string]int64, len(rows))
	for _, r := range rows {
		byLemma[r.Lemma] = r.ID
	}
	out := make([]int64, 0, len(lemmas))
	for _, lemma := range lemmas {
		if id, ok := byLemma[lemma]; ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// vocabUpsertSQL 生成该语种表的 upsert 语句；image_url 不在写入范围，保留管理台配图。
func vocabUpsertSQL(lang string) (string, []string) {
	table := vocabTableByLang(lang)
	columns := vocabImportColumns(lang)
	if table == "" || len(columns) == 0 {
		return "", nil
	}
	exprs, _ := vocabWriteArgs(VocabWordInput{}, columns)
	updates := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		updates = append(updates, column+" = EXCLUDED."+column)
	}
	updates = append(updates, "updated_at = now()")
	return fmt.Sprintf(
		`INSERT INTO %s (%s) VALUES (%s)
		 ON CONFLICT (lemma) DO UPDATE SET %s
		 RETURNING id, (xmax = 0) AS inserted`,
		table, strings.Join(columns, ", "), strings.Join(exprs, ", "), strings.Join(updates, ", "),
	), columns
}

type vocabUpsertResult struct {
	ID       int64 `gorm:"column:id"`
	Inserted bool  `gorm:"column:inserted"`
}

// UpsertVocabWords 在单个事务内批量 upsert，供 CSV 导入使用；返回新增与更新数量。
func (s *Store) UpsertVocabWords(ctx context.Context, rows []VocabWordInput) (int, int, error) {
	inserted, updated := 0, 0
	type upsertPlan struct {
		sql     string
		columns []string
	}
	plans := make(map[string]upsertPlan, len(vocabTables))
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			plan, ok := plans[row.Lang]
			if !ok {
				sql, columns := vocabUpsertSQL(row.Lang)
				if sql == "" {
					return ErrUnsupportedVocabLang
				}
				plan = upsertPlan{sql: sql, columns: columns}
				plans[row.Lang] = plan
			}
			_, args := vocabWriteArgs(row, plan.columns)
			var out vocabUpsertResult
			if err := tx.Raw(plan.sql, args...).Scan(&out).Error; err != nil {
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
