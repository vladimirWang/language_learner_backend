package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// 每日关卡
// ---------------------------------------------------------------------------

// DailyChallenge 对应 daily_challenges 一行；day 为 YYYY-MM-DD。
type DailyChallenge struct {
	ID        int64     `gorm:"column:id" json:"id"`
	Lang      string    `gorm:"column:lang" json:"lang"`
	Level     string    `gorm:"column:level" json:"level"`
	Day       string    `gorm:"column:day" json:"day"`
	Title     string    `gorm:"column:title" json:"title"`
	Status    string    `gorm:"column:status" json:"status"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
	ItemCount int       `gorm:"column:item_count" json:"item_count"`
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
	Passed      bool            `gorm:"column:passed" json:"passed"`
	CreatedAt   time.Time       `gorm:"column:created_at" json:"created_at"`
}

// UserStreak 对应 user_streaks 一行。
type UserStreak struct {
	OpenID    string  `gorm:"column:openid" json:"-"`
	Lang      string  `gorm:"column:lang" json:"lang"`
	Current   int     `gorm:"column:current" json:"current"`
	Longest   int     `gorm:"column:longest" json:"longest"`
	LastDay   *string `gorm:"column:last_day" json:"last_day"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"-"`
}

// ChallengeCalendarEntry 是用户视角的打卡日历一行。
type ChallengeCalendarEntry struct {
	Day         string `gorm:"column:day" json:"day"`
	ChallengeID int64  `gorm:"column:challenge_id" json:"challenge_id"`
	Title       string `gorm:"column:title" json:"title"`
	Status      string `gorm:"column:status" json:"status"`
	Score       *int   `gorm:"column:score" json:"score"`
	Total       *int   `gorm:"column:total" json:"total"`
	Passed      *bool  `gorm:"column:passed" json:"passed"`
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

// GetChallengeByDay 按 lang + level + day 读取关卡；不存在返回 ErrNotFound。
func (s *Store) GetChallengeByDay(ctx context.Context, lang, level, day string) (*DailyChallenge, error) {
	var ch DailyChallenge
	err := s.db.WithContext(ctx).Raw(
		`SELECT id, lang, level, to_char(day, 'YYYY-MM-DD') AS day, title, status,
		        created_at, updated_at,
		        (SELECT count(*) FROM daily_challenge_items i WHERE i.challenge_id = daily_challenges.id) AS item_count
		 FROM daily_challenges
		 WHERE lang = ? AND level = ? AND day = ?::date`,
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

// GetChallengeByID 按主键读取关卡。
func (s *Store) GetChallengeByID(ctx context.Context, id int64) (*DailyChallenge, error) {
	var ch DailyChallenge
	err := s.db.WithContext(ctx).Raw(
		`SELECT id, lang, level, to_char(day, 'YYYY-MM-DD') AS day, title, status,
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

// CreateChallenge 创建关卡与题目；若 (lang, level, day) 已存在则返回已有记录且 created=false。
func (s *Store) CreateChallenge(ctx context.Context, ch DailyChallenge, items []DailyChallengeItem) (*DailyChallenge, bool, error) {
	var id int64
	err := s.db.WithContext(ctx).Raw(
		`INSERT INTO daily_challenges (lang, level, day, title, status)
		 VALUES (?, ?, ?::date, ?, ?)
		 ON CONFLICT (lang, level, day) DO NOTHING
		 RETURNING id`,
		ch.Lang, ch.Level, ch.Day, ch.Title, ch.Status,
	).Scan(&id).Error
	if err != nil {
		return nil, false, err
	}
	if id == 0 {
		existing, err := s.GetChallengeByDay(ctx, ch.Lang, ch.Level, ch.Day)
		return existing, false, err
	}
	ch.ID = id
	if err := s.insertChallengeItems(ctx, id, items); err != nil {
		return nil, false, err
	}
	created, err := s.GetChallengeByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

func (s *Store) insertChallengeItems(ctx context.Context, challengeID int64, items []DailyChallengeItem) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		return nil
	})
}

// ReplaceChallengeItems 用新题目替换草稿关卡内容；已发布关卡返回 ErrChallengeLocked。
func (s *Store) ReplaceChallengeItems(ctx context.Context, challengeID int64, items []DailyChallengeItem) error {
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
		return tx.Exec(`UPDATE daily_challenges SET updated_at = now() WHERE id = ?`, challengeID).Error
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
		`SELECT id, openid, challenge_id, answers, score, total, passed, created_at
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

// SaveAttempt 写入作答记录；重复提交不覆盖，返回已有记录且 saved=false。
func (s *Store) SaveAttempt(ctx context.Context, a ChallengeAttempt) (*ChallengeAttempt, bool, error) {
	var id int64
	err := s.db.WithContext(ctx).Raw(
		`INSERT INTO challenge_attempts (openid, challenge_id, answers, score, total, passed)
		 VALUES (?, ?, ?::jsonb, ?, ?, ?)
		 ON CONFLICT (openid, challenge_id) DO NOTHING
		 RETURNING id`,
		a.OpenID, a.ChallengeID, string(a.Answers), a.Score, a.Total, a.Passed,
	).Scan(&id).Error
	if err != nil {
		return nil, false, err
	}
	if id == 0 {
		existing, err := s.GetAttempt(ctx, a.OpenID, a.ChallengeID)
		return existing, false, err
	}
	saved, err := s.GetAttempt(ctx, a.OpenID, a.ChallengeID)
	return saved, true, err
}

// UpdateStreakOnPass 在用户当天通关后更新连续打卡天数。
func (s *Store) UpdateStreakOnPass(ctx context.Context, openid, lang, day string) (UserStreak, error) {
	var out UserStreak
	err := s.db.WithContext(ctx).Raw(
		`INSERT INTO user_streaks (openid, lang, current, longest, last_day, updated_at)
		 VALUES (?, ?, 1, 1, ?::date, now())
		 ON CONFLICT (openid, lang) DO UPDATE SET
		     current = CASE
		         WHEN user_streaks.last_day = EXCLUDED.last_day THEN user_streaks.current
		         WHEN user_streaks.last_day = EXCLUDED.last_day - 1 THEN user_streaks.current + 1
		         ELSE 1
		     END,
		     longest = GREATEST(
		         user_streaks.longest,
		         CASE
		             WHEN user_streaks.last_day = EXCLUDED.last_day THEN user_streaks.current
		             WHEN user_streaks.last_day = EXCLUDED.last_day - 1 THEN user_streaks.current + 1
		             ELSE 1
		         END
		     ),
		     last_day = CASE
		         WHEN user_streaks.last_day = EXCLUDED.last_day THEN user_streaks.last_day
		         ELSE EXCLUDED.last_day
		     END,
		     updated_at = now()
		 RETURNING openid, lang, current, longest, to_char(last_day, 'YYYY-MM-DD') AS last_day`,
		openid, lang, day,
	).Scan(&out).Error
	return out, err
}

// GetStreak 读取用户某语种的连续打卡；无记录时返回零值。
func (s *Store) GetStreak(ctx context.Context, openid, lang string) (UserStreak, error) {
	out := UserStreak{OpenID: openid, Lang: lang}
	err := s.db.WithContext(ctx).Raw(
		`SELECT openid, lang, current, longest, to_char(last_day, 'YYYY-MM-DD') AS last_day, updated_at
		 FROM user_streaks WHERE openid = ? AND lang = ?`,
		openid, lang,
	).Scan(&out).Error
	return out, err
}

// NormalizeStreak 按今天的日期修正连续天数：昨天之前最后一次打卡视为断档，current 归零。
// 仅影响展示；数据库中的历史记录保持原样，下次通关时由 UpdateStreakOnPass 重置。
func NormalizeStreak(streak UserStreak, today string) UserStreak {
	if streak.LastDay == nil || *streak.LastDay == "" {
		streak.Current = 0
		return streak
	}
	todayDate, err := time.Parse("2006-01-02", today)
	if err != nil {
		return streak
	}
	lastDate, err := time.Parse("2006-01-02", *streak.LastDay)
	if err != nil {
		return streak
	}
	yesterday := todayDate.AddDate(0, 0, -1)
	if !lastDate.Equal(todayDate) && !lastDate.Equal(yesterday) {
		streak.Current = 0
	}
	return streak
}

// ChallengeCalendar 返回某月已发布关卡及当前用户的作答情况。
func (s *Store) ChallengeCalendar(ctx context.Context, openid, lang, level, from, to string) ([]ChallengeCalendarEntry, error) {
	entries := make([]ChallengeCalendarEntry, 0, 31)
	err := s.db.WithContext(ctx).Raw(
		`SELECT to_char(c.day, 'YYYY-MM-DD') AS day, c.id AS challenge_id, c.title, c.status,
		        a.score, a.total, a.passed
		 FROM daily_challenges c
		 LEFT JOIN challenge_attempts a ON a.challenge_id = c.id AND a.openid = ?
		 WHERE c.lang = ? AND c.level = ? AND c.status = 'published'
		   AND c.day >= ?::date AND c.day <= ?::date
		 ORDER BY c.day ASC`,
		openid, lang, level, from, to,
	).Scan(&entries).Error
	return entries, err
}

// ListChallengesForMonth 返回管理台某月的关卡列表（含草稿与题目数）。
func (s *Store) ListChallengesForMonth(ctx context.Context, lang, level, from, to string) ([]DailyChallenge, error) {
	rows := make([]DailyChallenge, 0, 31)
	q := s.db.WithContext(ctx).Raw(
		`SELECT c.id, c.lang, c.level, to_char(c.day, 'YYYY-MM-DD') AS day, c.title, c.status,
		        c.created_at, c.updated_at,
		        (SELECT count(*) FROM daily_challenge_items i WHERE i.challenge_id = c.id) AS item_count
		 FROM daily_challenges c
		 WHERE c.day >= ?::date AND c.day <= ?::date
		   AND (? = '' OR c.lang = ?)
		   AND (? = '' OR c.level = ?)
		 ORDER BY c.day ASC`,
		from, to, lang, lang, level, level,
	)
	if err := q.Scan(&rows).Error; err != nil {
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
		`SELECT DISTINCT lang, level FROM vocab_words
		 WHERE level <> ''
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
	err := s.db.WithContext(ctx).Raw(
		`SELECT id, lemma, chinese_mean, COALESCE(word_type, '') AS word_type,
		        COALESCE(example_de, '') AS example_de
		 FROM vocab_words
		 WHERE lang = ? AND level = ?
		 ORDER BY md5(lemma || ?) ASC, id ASC
		 LIMIT ?`,
		lang, level, seed, limit,
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
	err = s.db.WithContext(ctx).Raw(
		`SELECT id, lemma, chinese_mean, COALESCE(word_type, '') AS word_type,
		        COALESCE(example_de, '') AS example_de
		 FROM vocab_words
		 WHERE lang = ?
		 ORDER BY md5(lemma || ?) ASC, id ASC
		 LIMIT ?`,
		lang, seed, limit*2,
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
