package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// 每日关卡
// ---------------------------------------------------------------------------

// DailyChallenge 对应 daily_challenges 一行；day 为 YYYY-MM-DD。
type DailyChallenge struct {
	ID            int64     `gorm:"column:id" json:"id"`
	Lang          string    `gorm:"column:lang" json:"lang"`
	Level         string    `gorm:"column:level" json:"level"`
	Day           string    `gorm:"column:day" json:"day"`
	ChallengeType string    `gorm:"column:challenge_type" json:"challenge_type"`
	Title         string    `gorm:"column:title" json:"title"`
	Status        string    `gorm:"column:status" json:"status"`
	CreatedAt     time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at" json:"updated_at"`
	ItemCount     int       `gorm:"column:item_count" json:"item_count"`
	// Seq 是同一 (lang, level) 序列中的序号，仅管理台查询填充。
	Seq int `gorm:"column:seq" json:"seq,omitempty"`
	// ParticipantCount 是去重后的参与人数，仅管理台查询填充。
	ParticipantCount int `gorm:"column:participant_count" json:"participant_count,omitempty"`
}

// DailyChallengeItem 对应 daily_challenge_items 一行，prompt/answer 为 JSONB。
type DailyChallengeItem struct {
	ID          int64           `gorm:"column:id" json:"-"`
	ChallengeID int64           `gorm:"column:challenge_id" json:"-"`
	Seq         int             `gorm:"column:seq" json:"seq"`
	Kind        string          `gorm:"column:kind" json:"kind"`
	RefID       string          `gorm:"column:ref_id" json:"ref_id"`
	Prompt      json.RawMessage `gorm:"column:prompt" json:"prompt"`
	Answer      json.RawMessage `gorm:"column:answer" json:"-"`
	Explanation string          `gorm:"column:explanation" json:"explanation"`
}

// ChallengeAttempt 对应 challenge_attempts 一行。
type ChallengeAttempt struct {
	ID          int64           `gorm:"column:id" json:"-"`
	OpenID      string          `gorm:"column:openid" json:"-"`
	ChallengeID int64           `gorm:"column:challenge_id" json:"-"`
	Answers     json.RawMessage `gorm:"column:answers" json:"answers"`
	Score       int             `gorm:"column:score" json:"score"`
	Total       int             `gorm:"column:total" json:"total"`
	Stars       int             `gorm:"column:stars" json:"stars"`
	BestStars   int             `gorm:"column:best_stars" json:"best_stars"`
	Passed      bool            `gorm:"column:passed" json:"passed"`
	CreatedAt   time.Time       `gorm:"column:created_at" json:"created_at"`
}

// ChallengeSubmission 是一次完整提交事务的结果。
type ChallengeSubmission struct {
	Attempt          ChallengeAttempt
	StarsAdded       int
	TotalStars       int64
	AlreadySubmitted bool
}

// ChallengePassDay 是一条已打卡记录，用于启动时回填 Redis bitmap。
// day 取作答提交日（attempt.created_at），而非关卡生成日 ch.Day——打卡与日期解绑后，
// 用户哪天答成就算哪天打卡。
type ChallengePassDay struct {
	OpenID string `gorm:"column:openid"`
	Lang   string `gorm:"column:lang"`
	Day    string `gorm:"column:day"`
}

// ChallengeWord 是出题用的词条候选。
type ChallengeWord struct {
	ID          int64  `gorm:"column:id"`
	Lemma       string `gorm:"column:lemma"`
	ChineseMean string `gorm:"column:chinese_mean"`
	WordType    string `gorm:"column:word_type"`
	Example     string `gorm:"column:example_de"`
}

// Collocation 是固定搭配真值表的一行。
type Collocation struct {
	ID        int64  `gorm:"column:id" json:"id"`
	Lang      string `gorm:"column:lang" json:"lang"`
	Level     string `gorm:"column:level" json:"level"`
	Phrase    string `gorm:"column:phrase" json:"phrase"`
	MeaningZH string `gorm:"column:meaning_zh" json:"meaning_zh"`
	Example   string `gorm:"column:example" json:"example"`
}

const (
	// ChallengeStatusDraft 草稿；仅管理台可见。
	ChallengeStatusDraft = "draft"
	// ChallengeStatusPublished 已发布；学员端可见且内容锁定。
	ChallengeStatusPublished = "published"
)

// ErrChallengeLocked 表示关卡已发布或已有作答，不允许再改内容。
var ErrChallengeLocked = errors.New("challenge locked")

// GetChallengeByDay 按 lang + level + day 读取关卡（取该天最新一条，供管理台）；不存在返回 ErrNotFound。
func (s *Store) GetChallengeByDay(ctx context.Context, lang, level, day string) (*DailyChallenge, error) {
	var ch DailyChallenge
	err := s.db.WithContext(ctx).Raw(
		`SELECT id, lang, level, to_char(day, 'YYYY-MM-DD') AS day, challenge_type, title, status,
		        created_at, updated_at,
		        (SELECT count(*) FROM daily_challenge_items i WHERE i.challenge_id = daily_challenges.id) AS item_count
		 FROM daily_challenges
		 WHERE lang = ? AND level = ? AND day = ?::date
		 ORDER BY id DESC
		 LIMIT 1`,
		lang, level, day,
	).Scan(&ch).Error
	if err != nil {
		return nil, err
	}
	if ch.ID == 0 {
		return nil, ErrNotFound
	}
	return &ch, nil
}

// ListChallenges 返回同一 lang + level 下所有已发布关卡，按 id 升序构成有序序列（带题目数）。
func (s *Store) ListChallenges(ctx context.Context, lang, level string) ([]DailyChallenge, error) {
	rows := make([]DailyChallenge, 0, 64)
	err := s.db.WithContext(ctx).Raw(
		`SELECT c.id, c.lang, c.level, to_char(c.day, 'YYYY-MM-DD') AS day, c.challenge_type, c.title, c.status,
		        c.created_at, c.updated_at,
		        (SELECT count(*) FROM daily_challenge_items i WHERE i.challenge_id = c.id) AS item_count
		 FROM daily_challenges c
		 WHERE c.lang = ? AND c.level = ? AND c.status = 'published'
		 ORDER BY c.id ASC`,
		lang, level,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ListAttempts 批量读取用户在指定关卡上的作答记录，用于计算解锁状态。
func (s *Store) ListAttempts(ctx context.Context, openid string, challengeIDs []int64) ([]ChallengeAttempt, error) {
	out := make([]ChallengeAttempt, 0, len(challengeIDs))
	if len(challengeIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(challengeIDs))
	args := make([]any, 0, len(challengeIDs)+1)
	args = append(args, openid)
	for i, id := range challengeIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	err := s.db.WithContext(ctx).Raw(
		`SELECT id, openid, challenge_id, answers, score, total, stars, best_stars, passed, created_at
		 FROM challenge_attempts
		 WHERE openid = ? AND challenge_id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetChallengeByID 按主键读取关卡。
func (s *Store) GetChallengeByID(ctx context.Context, id int64) (*DailyChallenge, error) {
	var ch DailyChallenge
	err := s.db.WithContext(ctx).Raw(
		`SELECT id, lang, level, to_char(day, 'YYYY-MM-DD') AS day, challenge_type, title, status,
		        created_at, updated_at,
		        (SELECT count(*) FROM daily_challenge_items i WHERE i.challenge_id = daily_challenges.id) AS item_count
		 FROM daily_challenges WHERE id = ?`,
		id,
	).Scan(&ch).Error
	if err != nil {
		return nil, err
	}
	if ch.ID == 0 {
		return nil, ErrNotFound
	}
	return &ch, nil
}

// GetChallengeForAdmin 按主键读取关卡，附带序列序号与去重参与人数，供管理台详情使用。
func (s *Store) GetChallengeForAdmin(ctx context.Context, id int64) (*DailyChallenge, error) {
	var ch DailyChallenge
	err := s.db.WithContext(ctx).Raw(
		`SELECT c.id, c.lang, c.level, to_char(c.day, 'YYYY-MM-DD') AS day, c.challenge_type, c.title, c.status,
		        c.created_at, c.updated_at,
		        (SELECT count(*) FROM daily_challenge_items i WHERE i.challenge_id = c.id) AS item_count,
		        (SELECT count(DISTINCT a.openid) FROM challenge_attempts a WHERE a.challenge_id = c.id) AS participant_count,
		        (SELECT count(*) FROM daily_challenges x
		          WHERE x.lang = c.lang AND x.level = c.level AND x.id <= c.id)::int AS seq
		 FROM daily_challenges c WHERE c.id = ?`,
		id,
	).Scan(&ch).Error
	if err != nil {
		return nil, err
	}
	if ch.ID == 0 {
		return nil, ErrNotFound
	}
	return &ch, nil
}

// ChallengeSequenceNumber 返回关卡在 (lang, level) 序列中的序号（从 1 开始）。
func (s *Store) ChallengeSequenceNumber(ctx context.Context, lang, level string, id int64) (int, error) {
	var seq int
	err := s.db.WithContext(ctx).Raw(
		`SELECT count(*)::int FROM daily_challenges
		 WHERE lang = ? AND level = ? AND id <= ?`,
		lang, level, id,
	).Scan(&seq).Error
	return seq, err
}

// ListChallengeItems 按 seq 返回关卡题目。
func (s *Store) ListChallengeItems(ctx context.Context, challengeID int64) ([]DailyChallengeItem, error) {
	items := make([]DailyChallengeItem, 0, 8)
	err := s.db.WithContext(ctx).Raw(
		`SELECT id, challenge_id, seq, kind, ref_id, prompt, answer, explanation
		 FROM daily_challenge_items
		 WHERE challenge_id = ?
		 ORDER BY seq ASC`,
		challengeID,
	).Scan(&items).Error
	return items, err
}

// CreateChallenge 在一个事务内创建关卡与题目。(lang, level, day) 唯一约束已移除，
// 调用方需自行保证不重复创建（管理台先 GetChallengeByDay 查重）。
func (s *Store) CreateChallenge(ctx context.Context, ch DailyChallenge, items []DailyChallengeItem) (*DailyChallenge, bool, error) {
	if ch.ChallengeType == "" {
		ch.ChallengeType = ChallengeTypeVocab
	}
	var saved DailyChallenge
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var id int64
		if err := tx.Raw(
			`INSERT INTO daily_challenges (lang, level, day, challenge_type, title, status)
			 VALUES (?, ?, ?::date, ?, ?, ?)
			 RETURNING id`,
			ch.Lang, ch.Level, ch.Day, ch.ChallengeType, ch.Title, ch.Status,
		).Scan(&id).Error; err != nil {
			return err
		}
		for _, item := range items {
			if err := tx.Exec(
				`INSERT INTO daily_challenge_items
				     (challenge_id, seq, kind, ref_id, prompt, answer, explanation)
				 VALUES (?, ?, ?, ?, ?::jsonb, ?::jsonb, ?)`,
				id, item.Seq, item.Kind, item.RefID,
				string(item.Prompt), string(item.Answer), item.Explanation,
			).Error; err != nil {
				return err
			}
		}
		return tx.Raw(
			`SELECT id, lang, level, to_char(day, 'YYYY-MM-DD') AS day, challenge_type, title, status,
			        created_at, updated_at,
			        (SELECT count(*) FROM daily_challenge_items i WHERE i.challenge_id = daily_challenges.id) AS item_count
			 FROM daily_challenges WHERE id = ?`, id,
		).Scan(&saved).Error
	})
	if err != nil {
		return nil, false, err
	}
	return &saved, true, nil
}

// ReplaceChallengeContent 原子替换草稿的题型、标题与题目；已发布关卡返回 ErrChallengeLocked。
func (s *Store) ReplaceChallengeContent(ctx context.Context, challengeID int64, challengeType, title string, items []DailyChallengeItem) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var status string
		if err := tx.Raw(`SELECT status FROM daily_challenges WHERE id = ?`, challengeID).Scan(&status).Error; err != nil {
			return err
		}
		if status == "" {
			return ErrNotFound
		}
		if status == ChallengeStatusPublished {
			return ErrChallengeLocked
		}
		if err := tx.Exec(`DELETE FROM daily_challenge_items WHERE challenge_id = ?`, challengeID).Error; err != nil {
			return err
		}
		for _, item := range items {
			if err := tx.Exec(
				`INSERT INTO daily_challenge_items
				     (challenge_id, seq, kind, ref_id, prompt, answer, explanation)
				 VALUES (?, ?, ?, ?, ?::jsonb, ?::jsonb, ?)`,
				challengeID, item.Seq, item.Kind, item.RefID,
				string(item.Prompt), string(item.Answer), item.Explanation,
			).Error; err != nil {
				return err
			}
		}
		return tx.Exec(
			`UPDATE daily_challenges SET challenge_type = ?, title = ?, updated_at = now() WHERE id = ?`,
			challengeType, title, challengeID,
		).Error
	})
}

// UpdateChallenge 更新标题与状态；发布后不允许改回草稿（内容锁定）。
func (s *Store) UpdateChallenge(ctx context.Context, challengeID int64, title, status *string) (*DailyChallenge, error) {
	ch, err := s.GetChallengeByID(ctx, challengeID)
	if err != nil {
		return nil, err
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if title != nil {
		if ch.Status == ChallengeStatusPublished {
			return nil, ErrChallengeLocked
		}
		updates["title"] = *title
	}
	if status != nil {
		if *status != ChallengeStatusDraft && *status != ChallengeStatusPublished {
			return nil, errors.New("invalid status")
		}
		if ch.Status == ChallengeStatusPublished && *status == ChallengeStatusDraft {
			has, err := s.ChallengeHasAttempts(ctx, challengeID)
			if err != nil {
				return nil, err
			}
			if has {
				return nil, ErrChallengeLocked
			}
		}
		updates["status"] = *status
	}
	if err := s.db.WithContext(ctx).Table("daily_challenges").
		Where("id = ?", challengeID).Updates(updates).Error; err != nil {
		return nil, err
	}
	return s.GetChallengeByID(ctx, challengeID)
}

// ChallengeHasAttempts 判断关卡是否已有用户作答。
func (s *Store) ChallengeHasAttempts(ctx context.Context, challengeID int64) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Table("challenge_attempts").
		Where("challenge_id = ?", challengeID).Count(&count).Error
	return count > 0, err
}

// GetAttempt 读取用户在某关卡的作答记录；不存在返回 ErrNotFound。
func (s *Store) GetAttempt(ctx context.Context, openid string, challengeID int64) (*ChallengeAttempt, error) {
	var a ChallengeAttempt
	err := s.db.WithContext(ctx).Raw(
		`SELECT id, openid, challenge_id, answers, score, total, stars, best_stars, passed, created_at
		 FROM challenge_attempts WHERE openid = ? AND challenge_id = ?`,
		openid, challengeID,
	).Scan(&a).Error
	if err != nil {
		return nil, err
	}
	if a.ID == 0 {
		return nil, ErrNotFound
	}
	return &a, nil
}

// SubmitChallengeAttempt 保存最新作答，并在同一事务内维护最高星与累计星。
// 对微信用户行加锁可串行化同一用户的提交，避免并发重复补差或丢失累计更新。
func (s *Store) SubmitChallengeAttempt(ctx context.Context, a ChallengeAttempt) (ChallengeSubmission, error) {
	var out ChallengeSubmission
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user struct {
			ID         int64 `gorm:"column:id"`
			TotalStars int64 `gorm:"column:total_stars"`
		}
		if err := tx.Raw(`SELECT id, total_stars FROM wx_users WHERE openid = ? FOR UPDATE`, a.OpenID).Scan(&user).Error; err != nil {
			return err
		}
		if user.ID == 0 {
			return ErrNotFound
		}
		var challengeID int64
		if err := tx.Raw(`SELECT id FROM daily_challenges WHERE id = ? FOR KEY SHARE`, a.ChallengeID).Scan(&challengeID).Error; err != nil {
			return err
		}
		if challengeID == 0 {
			return ErrNotFound
		}

		var previous ChallengeAttempt
		if err := tx.Raw(
			`SELECT id, openid, challenge_id, answers, score, total, stars, best_stars, passed, created_at
			 FROM challenge_attempts WHERE openid = ? AND challenge_id = ? FOR UPDATE`,
			a.OpenID, a.ChallengeID,
		).Scan(&previous).Error; err != nil {
			return err
		}
		out.AlreadySubmitted = previous.ID != 0
		oldBest := previous.BestStars
		out.StarsAdded = a.Stars - oldBest
		if out.StarsAdded < 0 {
			out.StarsAdded = 0
		}

		if err := tx.Raw(
			`INSERT INTO challenge_attempts
			     (openid, challenge_id, answers, score, total, stars, best_stars, passed)
			 VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, ?)
			 ON CONFLICT (openid, challenge_id) DO UPDATE SET
			     answers = EXCLUDED.answers,
			     score = EXCLUDED.score,
			     total = EXCLUDED.total,
			     stars = EXCLUDED.stars,
			     best_stars = GREATEST(challenge_attempts.best_stars, EXCLUDED.stars),
			     passed = challenge_attempts.passed OR EXCLUDED.passed
			 RETURNING id, openid, challenge_id, answers, score, total, stars, best_stars, passed, created_at`,
			a.OpenID, a.ChallengeID, string(a.Answers), a.Score, a.Total,
			a.Stars, a.Stars, a.Stars >= 2,
		).Scan(&out.Attempt).Error; err != nil {
			return err
		}

		out.TotalStars = user.TotalStars
		if out.StarsAdded > 0 {
			if err := tx.Raw(
				`UPDATE wx_users SET total_stars = total_stars + ? WHERE id = ? RETURNING total_stars`,
				out.StarsAdded, user.ID,
			).Scan(&out.TotalStars).Error; err != nil {
				return err
			}
		}

		return nil
	})
	return out, err
}

// PassedChallengeDays 返回所有已打卡的 (openid, lang, day)，供启动时回填 Redis bitmap。
// day 取作答提交日（attempt.created_at），DISTINCT 去重：同一天通过多关只回填一次。
func (s *Store) PassedChallengeDays(ctx context.Context) ([]ChallengePassDay, error) {
	days := make([]ChallengePassDay, 0, 256)
	err := s.db.WithContext(ctx).Raw(
		`SELECT DISTINCT a.openid, c.lang, to_char(a.created_at, 'YYYY-MM-DD') AS day
		 FROM challenge_attempts a
		 JOIN daily_challenges c ON c.id = a.challenge_id
		 WHERE a.passed
		 ORDER BY day ASC`,
	).Scan(&days).Error
	return days, err
}

// ListChallengesForAdmin 返回管理台关卡序列（含草稿），按 id 升序，附序号、题目数与去重参与人数。
// 关卡已与日期解绑，lang / level 为空时返回全部。
func (s *Store) ListChallengesForAdmin(ctx context.Context, lang, level string) ([]DailyChallenge, error) {
	rows := make([]DailyChallenge, 0, 64)
	err := s.db.WithContext(ctx).Raw(
		`SELECT c.id, c.lang, c.level, to_char(c.day, 'YYYY-MM-DD') AS day, c.challenge_type, c.title, c.status,
		        c.created_at, c.updated_at,
		        (SELECT count(*) FROM daily_challenge_items i WHERE i.challenge_id = c.id) AS item_count,
		        (SELECT count(DISTINCT a.openid) FROM challenge_attempts a WHERE a.challenge_id = c.id) AS participant_count,
		        (row_number() OVER (PARTITION BY c.lang, c.level ORDER BY c.id))::int AS seq
		 FROM daily_challenges c
		 WHERE (? = '' OR c.lang = ?)
		   AND (? = '' OR c.level = ?)
		 ORDER BY c.lang ASC, c.level ASC, c.id ASC`,
		lang, lang, level, level,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ChallengeTargets 返回需要生成关卡的 (lang, level) 组合，来自新词库的可用等级。
func (s *Store) ChallengeTargets(ctx context.Context) ([][2]string, error) {
	type row struct {
		Lang  string `gorm:"column:lang"`
		Level string `gorm:"column:level"`
	}
	rows := make([]row, 0, 32)
	if err := s.db.WithContext(ctx).Raw(
		`SELECT DISTINCT 'de' AS lang, level FROM vocab_words_de WHERE level <> '' AND deleted_at IS NULL
		 UNION ALL SELECT DISTINCT 'en' AS lang, level FROM vocab_words_en WHERE level <> '' AND deleted_at IS NULL
		 UNION ALL SELECT DISTINCT 'ja' AS lang, level FROM vocab_words_ja WHERE level <> '' AND deleted_at IS NULL
		 ORDER BY lang ASC, level ASC`,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([][2]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, [2]string{r.Lang, r.Level})
	}
	return out, nil
}

// ChallengeWordPool 返回按日期稳定打散的词条候选；优先当前等级，不足时补同语种词条。
func (s *Store) ChallengeWordPool(ctx context.Context, lang, level, seed string, limit int) ([]ChallengeWord, error) {
	words := make([]ChallengeWord, 0, limit)
	table := vocabTableByLang(lang)
	if table == "" {
		return words, nil
	}
	// 各语种例句列不同，统一别名成 example_de 供 ChallengeWord 使用。
	example := vocabExampleColumn(lang)
	err := s.db.WithContext(ctx).Raw(fmt.Sprintf(
		`SELECT id, lemma, chinese_mean, COALESCE(word_type, '') AS word_type,
		        COALESCE(%s, '') AS example_de
		 FROM %s
		 WHERE level = ? AND deleted_at IS NULL
		 ORDER BY md5(lemma || ?) ASC, id ASC
		 LIMIT ?`, example, table),
		level, seed, limit,
	).Scan(&words).Error
	if err != nil {
		return nil, err
	}
	if len(words) >= limit {
		return words, nil
	}
	seen := make(map[int64]bool, len(words))
	for _, w := range words {
		seen[w.ID] = true
	}
	extra := make([]ChallengeWord, 0, limit-len(words))
	err = s.db.WithContext(ctx).Raw(fmt.Sprintf(
		`SELECT id, lemma, chinese_mean, COALESCE(word_type, '') AS word_type,
		        COALESCE(%s, '') AS example_de
		 FROM %s
		 WHERE deleted_at IS NULL
		 ORDER BY md5(lemma || ?) ASC, id ASC
		 LIMIT ?`, example, table),
		seed, limit*2,
	).Scan(&extra).Error
	if err != nil {
		return words, err
	}
	for _, w := range extra {
		if len(words) >= limit {
			break
		}
		if seen[w.ID] {
			continue
		}
		seen[w.ID] = true
		words = append(words, w)
	}
	return words, nil
}

// ChallengeCollocationPool 返回按日期稳定打散的搭配候选；优先当前等级，不足时补同语种。
func (s *Store) ChallengeCollocationPool(ctx context.Context, lang, level, seed string, limit int) ([]Collocation, error) {
	list := make([]Collocation, 0, limit)
	err := s.db.WithContext(ctx).Raw(
		`SELECT id, lang, level, phrase, meaning_zh, example
		 FROM collocations
		 WHERE lang = ? AND (level = ? OR level = '')
		 ORDER BY md5(phrase || ?) ASC, id ASC
		 LIMIT ?`,
		lang, level, seed, limit,
	).Scan(&list).Error
	if err != nil {
		return nil, err
	}
	if len(list) >= limit {
		return list, nil
	}
	seen := make(map[int64]bool, len(list))
	for _, c := range list {
		seen[c.ID] = true
	}
	extra := make([]Collocation, 0, limit-len(list))
	err = s.db.WithContext(ctx).Raw(
		`SELECT id, lang, level, phrase, meaning_zh, example
		 FROM collocations
		 WHERE lang = ?
		 ORDER BY md5(phrase || ?) ASC, id ASC
		 LIMIT ?`,
		lang, seed, limit*2,
	).Scan(&extra).Error
	if err != nil {
		return list, err
	}
	for _, c := range extra {
		if len(list) >= limit {
			break
		}
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		list = append(list, c)
	}
	return list, nil
}

const (
	// ChallengeTypeVocab 单词 + 固定搭配。
	ChallengeTypeVocab = "vocab"
	// ChallengeTypeGrammar 纯语法题。
	ChallengeTypeGrammar = "grammar"
	// ChallengeTypeListening 纯听力题。
	ChallengeTypeListening = "listening"
)

// GrammarQuestion 是结构化语法题库的一行。
type GrammarQuestion struct {
	ID          int64           `gorm:"column:id" json:"id"`
	Lang        string          `gorm:"column:lang" json:"lang"`
	Level       string          `gorm:"column:level" json:"level"`
	PointKey    string          `gorm:"column:point_key" json:"point_key"`
	PointLabel  string          `gorm:"column:point_label" json:"point_label"`
	Stem        string          `gorm:"column:stem" json:"stem"`
	Options     json.RawMessage `gorm:"column:options" json:"options"`
	Answer      string          `gorm:"column:answer" json:"answer"`
	Explanation string          `gorm:"column:explanation" json:"explanation"`
	SourcePath  string          `gorm:"column:source_path" json:"source_path"`
	SourceSHA   string          `gorm:"column:source_sha" json:"source_sha"`
	GeneratedBy string          `gorm:"column:generated_by" json:"generated_by"`
	Status      string          `gorm:"column:status" json:"status"`
	ReviewNote  string          `gorm:"column:review_note" json:"review_note"`
	ReviewedAt  *time.Time      `gorm:"column:reviewed_at" json:"reviewed_at"`
}

// 语法题审核状态。
const (
	GrammarStatusDraft    = "draft"
	GrammarStatusApproved = "approved"
	GrammarStatusRejected = "rejected"
)

// ChallengeGrammarPool 返回结构化语法题候选；优先当前等级，不足时补同语种。
func (s *Store) ChallengeGrammarPool(ctx context.Context, lang, level, seed string, limit int) ([]GrammarQuestion, error) {
	out := make([]GrammarQuestion, 0, limit)
	err := s.db.WithContext(ctx).Raw(
		`SELECT id, lang, level, point_key, point_label, stem, options,
		        answer, explanation, source_path
		 FROM grammar_questions
		 WHERE lang = ? AND deleted_at IS NULL AND active AND status = 'approved' AND (level = ? OR level = '')
		 ORDER BY md5(stem || ?) ASC, id ASC
		 LIMIT ?`,
		lang, level, seed, limit,
	).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if len(out) >= limit {
		return out, nil
	}
	seen := make(map[int64]bool, len(out))
	for _, q := range out {
		seen[q.ID] = true
	}
	extra := make([]GrammarQuestion, 0, limit-len(out))
	err = s.db.WithContext(ctx).Raw(
		`SELECT id, lang, level, point_key, point_label, stem, options,
		        answer, explanation, source_path
		 FROM grammar_questions
		 WHERE lang = ? AND deleted_at IS NULL AND active AND status = 'approved'
		 ORDER BY md5(stem || ?) ASC, id ASC
		 LIMIT ?`,
		lang, seed, limit*2,
	).Scan(&extra).Error
	if err != nil {
		return out, err
	}
	for _, q := range extra {
		if len(out) >= limit {
			break
		}
		if seen[q.ID] {
			continue
		}
		seen[q.ID] = true
		out = append(out, q)
	}
	return out, nil
}

// ChallengeListeningJobs 返回可作为听力题源的活跃任务；优先同等级。
func (s *Store) ChallengeListeningJobs(ctx context.Context, lang, level string) ([]ListeningJob, error) {
	jobs := make([]ListeningJob, 0, 16)
	err := s.db.WithContext(ctx).Raw(
		`SELECT job_id, exam_id, name, lang, level, source_url, status, manifest_path,
		        log_path, error_msg, last_log_at, deleted_at, has_answers, created_at, updated_at
		 FROM listening_jobs
		 WHERE lang = ? AND deleted_at IS NULL AND status = 'built'
		 ORDER BY (level = ?) DESC, exam_id ASC`,
		lang, level,
	).Scan(&jobs).Error
	return jobs, err
}

// GetChallengeItem 按主键读取单道题，供音频下发校验路径。
func (s *Store) GetChallengeItem(ctx context.Context, id int64) (*DailyChallengeItem, error) {
	var item DailyChallengeItem
	err := s.db.WithContext(ctx).Raw(
		`SELECT id, challenge_id, seq, kind, ref_id, prompt, answer, explanation
		 FROM daily_challenge_items WHERE id = ?`, id,
	).Scan(&item).Error
	if err != nil {
		return nil, err
	}
	if item.ID == 0 {
		return nil, ErrNotFound
	}
	return &item, nil
}
