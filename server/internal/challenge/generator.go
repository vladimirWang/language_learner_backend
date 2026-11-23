// Package challenge 生成每日关卡：用真值表（词库、固定搭配、语法点）加模板出题，
// 不调用模型。生成的题目以快照写入 daily_challenge_items，发布后内容锁定。
package challenge

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wangfernando/language-learner/server/internal/store"
)

const (
	wordsPerChallenge        = 3
	collocationsPerChallenge = 2
	grammarPerChallenge      = 1
	totalItems               = wordsPerChallenge + collocationsPerChallenge + grammarPerChallenge
	// 每题固定 4 个选项；候选不足时按实际数量下发。
	optionsPerQuestion = 4
)

// ErrNoContent 表示真值库素材不足，无法生成关卡。
var ErrNoContent = errors.New("challenge content not ready")

// Builder 负责从 store 读取素材并生成关卡快照。
type Builder struct {
	st *store.Store
}

// NewBuilder 创建关卡生成器。
func NewBuilder(st *store.Store) *Builder {
	return &Builder{st: st}
}

// choicePrompt 是选择题题面。
type choicePrompt struct {
	Type     string   `json:"type"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

// choiceAnswer 是标准答案。
type choiceAnswer struct {
	Value string `json:"value"`
}

// EnsureDay 返回指定日期的关卡；不存在时生成。publish=true 时直接发布。
func (b *Builder) EnsureDay(ctx context.Context, lang, level string, day time.Time, publish bool) (*store.DailyChallenge, []store.DailyChallengeItem, error) {
	dayStr := day.Format("2006-01-02")
	existing, err := b.st.GetChallengeByDay(ctx, lang, level, dayStr)
	if err == nil {
		items, err := b.st.ListChallengeItems(ctx, existing.ID)
		return existing, items, err
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, nil, err
	}

	items, title, err := b.build(ctx, lang, level, dayStr)
	if err != nil {
		return nil, nil, err
	}
	status := store.ChallengeStatusDraft
	if publish {
		status = store.ChallengeStatusPublished
	}
	created, wasCreated, err := b.st.CreateChallenge(ctx, store.DailyChallenge{
		Lang:   lang,
		Level:  level,
		Day:    dayStr,
		Title:  title,
		Status: status,
	}, items)
	if err != nil {
		return nil, nil, err
	}
	if !wasCreated {
		// 并发请求下另一个协程先写入了，读取已有内容即可。
		items, err = b.st.ListChallengeItems(ctx, created.ID)
		return created, items, err
	}
	return created, items, nil
}

// Regenerate 重新生成草稿关卡内容；已发布关卡返回 store.ErrChallengeLocked。
func (b *Builder) Regenerate(ctx context.Context, lang, level string, day time.Time) (*store.DailyChallenge, []store.DailyChallengeItem, error) {
	dayStr := day.Format("2006-01-02")
	existing, err := b.st.GetChallengeByDay(ctx, lang, level, dayStr)
	if errors.Is(err, store.ErrNotFound) {
		return b.EnsureDay(ctx, lang, level, day, false)
	}
	if err != nil {
		return nil, nil, err
	}
	if existing.Status == store.ChallengeStatusPublished {
		return nil, nil, store.ErrChallengeLocked
	}
	items, title, err := b.build(ctx, lang, level, dayStr)
	if err != nil {
		return nil, nil, err
	}
	if err := b.st.ReplaceChallengeItems(ctx, existing.ID, items); err != nil {
		return nil, nil, err
	}
	if _, err := b.st.UpdateChallenge(ctx, existing.ID, &title, nil); err != nil {
		return nil, nil, err
	}
	updated, err := b.st.GetChallengeByID(ctx, existing.ID)
	if err != nil {
		return nil, nil, err
	}
	items, err = b.st.ListChallengeItems(ctx, existing.ID)
	return updated, items, err
}

// GenerateDay 为所有已配置的 (lang, level) 生成当天关卡；多用于定时空跑。
func (b *Builder) GenerateDay(ctx context.Context, day time.Time) []error {
	targets, err := b.st.ChallengeTargets(ctx)
	if err != nil {
		return []error{err}
	}
	errs := make([]error, 0)
	for _, target := range targets {
		if _, _, err := b.EnsureDay(ctx, target[0], target[1], day, true); err != nil && !errors.Is(err, ErrNoContent) {
			errs = append(errs, fmt.Errorf("%s/%s: %w", target[0], target[1], err))
		}
	}
	return errs
}

func (b *Builder) build(ctx context.Context, lang, level, dayStr string) ([]store.DailyChallengeItem, string, error) {
	seed := dayStr + "|" + lang + "|" + level
	items := make([]store.DailyChallengeItem, 0, totalItems)

	wordItems, err := b.buildWordItems(ctx, lang, level, seed, dayStr)
	if err != nil {
		return nil, "", err
	}
	items = append(items, wordItems...)

	collocationItems, err := b.buildCollocationItems(ctx, lang, level, seed)
	if err != nil {
		return nil, "", err
	}
	items = append(items, collocationItems...)

	grammarItem, err := b.buildGrammarItem(ctx, lang, level, seed)
	if err != nil && !errors.Is(err, ErrNoContent) {
		return nil, "", err
	}
	if grammarItem != nil {
		items = append(items, *grammarItem)
	}

	if len(items) < 3 {
		return nil, "", ErrNoContent
	}
	for i := range items {
		items[i].Seq = i + 1
	}
	title := fmt.Sprintf("%s %s 每日关卡", dayStr[5:], level)
	return items, title, nil
}

func (b *Builder) buildWordItems(ctx context.Context, lang, level, seed, dayStr string) ([]store.DailyChallengeItem, error) {
	pool, err := b.st.ChallengeWordPool(ctx, lang, level, seed, wordsPerChallenge+8)
	if err != nil {
		return nil, err
	}
	items := make([]store.DailyChallengeItem, 0, wordsPerChallenge)
	usedLemmas := make(map[string]bool)
	usedMeans := make(map[string]bool)
	for _, w := range pool {
		if len(items) >= wordsPerChallenge {
			break
		}
		mean := strings.TrimSpace(w.ChineseMean)
		if mean == "" || usedLemmas[w.Lemma] || usedMeans[strings.ToLower(mean)] {
			continue
		}
		usedLemmas[w.Lemma] = true
		usedMeans[strings.ToLower(mean)] = true

		options := []string{mean}
		for _, other := range pool {
			if len(options) >= optionsPerQuestion {
				break
			}
			distractor := strings.TrimSpace(other.ChineseMean)
			if distractor == "" || strings.EqualFold(distractor, mean) || containsFold(options, distractor) {
				continue
			}
			options = append(options, distractor)
		}

		question := fmt.Sprintf("「%s」的中文意思是？", w.Lemma)
		item, err := newChoiceItem("word", fmt.Sprintf("word:%d", w.ID), question, options, mean, wordExplanation(w))
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, ErrNoContent
	}
	return items, nil
}

func (b *Builder) buildCollocationItems(ctx context.Context, lang, level, seed string) ([]store.DailyChallengeItem, error) {
	pool, err := b.st.ChallengeCollocationPool(ctx, lang, level, seed, collocationsPerChallenge+8)
	if err != nil {
		return nil, err
	}
	items := make([]store.DailyChallengeItem, 0, collocationsPerChallenge)
	usedPhrases := make(map[string]bool)
	for _, c := range pool {
		if len(items) >= collocationsPerChallenge {
			break
		}
		phrase := strings.TrimSpace(c.Phrase)
		if phrase == "" || usedPhrases[phrase] {
			continue
		}
		usedPhrases[phrase] = true

		options := []string{phrase}
		for _, other := range pool {
			if len(options) >= optionsPerQuestion {
				break
			}
			distractor := strings.TrimSpace(other.Phrase)
			if distractor == "" || strings.EqualFold(distractor, phrase) || containsFold(options, distractor) {
				continue
			}
			options = append(options, distractor)
		}
		question := fmt.Sprintf("「%s」对应的固定搭配是？", c.MeaningZH)
		explanation := fmt.Sprintf("%s —— %s", phrase, c.MeaningZH)
		if c.Example != "" {
			explanation += "。例句：" + c.Example
		}
		item, err := newChoiceItem("collocation", fmt.Sprintf("collocation:%d", c.ID), question, options, phrase, explanation)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (b *Builder) buildGrammarItem(ctx context.Context, lang, level, seed string) (*store.DailyChallengeItem, error) {
	points, err := b.st.ListGrammarPoints(ctx, lang, level)
	if err != nil {
		return nil, err
	}
	candidates := make([]store.GrammarPoint, 0, len(points))
	for _, p := range points {
		if len(p.Prompts) > 0 && p.Label != "" {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) < 2 {
		return nil, ErrNoContent
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return hashHex(seed+candidates[i].Key) < hashHex(seed+candidates[j].Key)
	})
	if len(candidates) > optionsPerQuestion {
		candidates = candidates[:optionsPerQuestion]
	}

	correct := candidates[0]
	options := make([]string, 0, len(candidates))
	options = append(options, correct.Prompts[0])
	for _, p := range candidates[1:] {
		prompt := p.Prompts[0]
		if containsFold(options, prompt) {
			continue
		}
		options = append(options, prompt)
	}
	if len(options) < 2 {
		return nil, ErrNoContent
	}
	question := fmt.Sprintf("下面哪一句体现了「%s」？", correct.Label)
	explanation := fmt.Sprintf("考点：%s。%s", correct.Label, correct.Prompts[0])
	item, err := newChoiceItem("grammar", "grammar:"+correct.Key, question, options, correct.Prompts[0], explanation)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func newChoiceItem(kind, refID, question string, options []string, correct, explanation string) (store.DailyChallengeItem, error) {
	options = shuffleOptions(options, refID)
	prompt, err := json.Marshal(choicePrompt{Type: "choice", Question: question, Options: options})
	if err != nil {
		return store.DailyChallengeItem{}, err
	}
	answer, err := json.Marshal(choiceAnswer{Value: correct})
	if err != nil {
		return store.DailyChallengeItem{}, err
	}
	return store.DailyChallengeItem{
		Kind:        kind,
		RefID:       refID,
		Prompt:      prompt,
		Answer:      answer,
		Explanation: explanation,
	}, nil
}

// shuffleOptions 用题目 refID 做稳定打散，保证同一天同题顺序固定，便于缓存与复现。
func shuffleOptions(options []string, seed string) []string {
	out := append([]string(nil), options...)
	sort.SliceStable(out, func(i, j int) bool {
		return hashHex(seed+out[i]) < hashHex(seed+out[j])
	})
	return out
}

func wordExplanation(w store.ChallengeWord) string {
	parts := make([]string, 0, 3)
	if w.WordType != "" {
		parts = append(parts, "词性："+w.WordType)
	}
	parts = append(parts, fmt.Sprintf("%s = %s", w.Lemma, w.ChineseMean))
	if w.Example != "" {
		parts = append(parts, "例句："+w.Example)
	}
	return strings.Join(parts, "；")
}

func hashHex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func containsFold(list []string, target string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
