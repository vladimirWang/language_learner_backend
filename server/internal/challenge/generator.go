// Package challenge 生成每日关卡：每关只出一种题型，按 单词/搭配 → 语法 → 听力 轮转。
// 题目以快照写入 daily_challenge_items，发布后内容锁定。
package challenge

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wangfernando/language-learner/server/internal/store"
)

const (
	wordsPerChallenge        = 3
	collocationsPerChallenge = 3
	itemsPerChallenge        = 6
	// 每题固定 4 个选项；候选不足时按实际数量下发。
	optionsPerQuestion = 4
)

// challengeCycle 是固定的题型轮转顺序。
var challengeCycle = []string{
	store.ChallengeTypeVocab,
	store.ChallengeTypeGrammar,
	store.ChallengeTypeListening,
}

// cycleAnchor 是轮转起点：该日生成单词/搭配关。
var cycleAnchor = time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)

// ErrNoContent 表示真值库素材不足，无法生成关卡。
var ErrNoContent = errors.New("challenge content not ready")

// Builder 负责从 store 读取素材并生成关卡快照。
type Builder struct {
	st *store.Store
	// workDir 是听力产物根目录，manifest 位于 workDir/listening/{exam_id}/manifest.json。
	workDir string
}

// NewBuilder 创建关卡生成器。
func NewBuilder(st *store.Store) *Builder {
	workDir := os.Getenv("WORK_DIR")
	if workDir == "" {
		workDir = "work"
	}
	return &Builder{st: st, workDir: workDir}
}

// challengeTypeForDay 按固定顺序计算某天的题型。
func challengeTypeForDay(day time.Time) string {
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	days := int(d.Sub(cycleAnchor).Hours() / 24)
	index := ((days % len(challengeCycle)) + len(challengeCycle)) % len(challengeCycle)
	return challengeCycle[index]
}

// typeLabel 返回关卡题型的中文名。
func typeLabel(challengeType string) string {
	switch challengeType {
	case store.ChallengeTypeGrammar:
		return "语法"
	case store.ChallengeTypeListening:
		return "听力"
	default:
		return "单词与搭配"
	}
}

// choicePrompt 是选择题题面。
type choicePrompt struct {
	Type     string   `json:"type"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

// audioChoicePrompt 是听力题题面；audio 为相对 workDir 的路径。
type audioChoicePrompt struct {
	Type     string   `json:"type"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Audio    string   `json:"audio"`
	Duration float64  `json:"duration"`
}

// choiceAnswer 是标准答案。
type choiceAnswer struct {
	Value string `json:"value"`
}

// EnsureDay 返回指定日期的关卡；不存在时生成。publish=true 时直接发布。
// 当轮到的题型素材不足时，按轮转顺序降级到下一可用题型。
func (b *Builder) EnsureDay(ctx context.Context, lang, level string, day time.Time, publish bool) (*store.DailyChallenge, []store.DailyChallengeItem, error) {
	return b.ensureDay(ctx, lang, level, day, publish, "")
}

// ensureDay 在 requestedType 非空时只尝试指定题型，否则按日期轮转并允许素材不足时降级。
func (b *Builder) ensureDay(ctx context.Context, lang, level string, day time.Time, publish bool, requestedType string) (*store.DailyChallenge, []store.DailyChallengeItem, error) {
	dayStr := day.Format("2006-01-02")
	existing, err := b.st.GetChallengeByDay(ctx, lang, level, dayStr)
	if err == nil {
		items, err := b.st.ListChallengeItems(ctx, existing.ID)
		return existing, items, err
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, nil, err
	}

	status := store.ChallengeStatusDraft
	if publish {
		status = store.ChallengeStatusPublished
	}

	candidateTypes := make([]string, 0, len(challengeCycle))
	if requestedType != "" {
		candidateTypes = append(candidateTypes, requestedType)
	} else {
		start := 0
		for i, t := range challengeCycle {
			if t == challengeTypeForDay(day) {
				start = i
				break
			}
		}
		for offset := 0; offset < len(challengeCycle); offset++ {
			candidateTypes = append(candidateTypes, challengeCycle[(start+offset)%len(challengeCycle)])
		}
	}
	for _, challengeType := range candidateTypes {
		items, buildErr := b.build(ctx, lang, level, dayStr, challengeType)
		if errors.Is(buildErr, ErrNoContent) {
			continue
		}
		if buildErr != nil {
			return nil, nil, buildErr
		}
		title := fmt.Sprintf("%s %s %s关卡", dayStr[5:], level, typeLabel(challengeType))
		created, wasCreated, err := b.st.CreateChallenge(ctx, store.DailyChallenge{
			Lang:          lang,
			Level:         level,
			Day:           dayStr,
			ChallengeType: challengeType,
			Title:         title,
			Status:        status,
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
	return nil, nil, ErrNoContent
}

// Regenerate 生成或重新生成草稿；requestedType 为空时使用已有题型或日期默认题型。
func (b *Builder) Regenerate(ctx context.Context, lang, level string, day time.Time, requestedType string) (*store.DailyChallenge, []store.DailyChallengeItem, error) {
	dayStr := day.Format("2006-01-02")
	existing, err := b.st.GetChallengeByDay(ctx, lang, level, dayStr)
	if errors.Is(err, store.ErrNotFound) {
		return b.ensureDay(ctx, lang, level, day, false, requestedType)
	}
	if err != nil {
		return nil, nil, err
	}
	if existing.Status == store.ChallengeStatusPublished {
		return nil, nil, store.ErrChallengeLocked
	}
	challengeType := requestedType
	if challengeType == "" {
		challengeType = existing.ChallengeType
	}
	if challengeType == "" {
		challengeType = challengeTypeForDay(day)
	}
	items, err := b.build(ctx, lang, level, dayStr, challengeType)
	if err != nil {
		return nil, nil, err
	}
	title := fmt.Sprintf("%s %s %s关卡", dayStr[5:], level, typeLabel(challengeType))
	if err := b.st.ReplaceChallengeContent(ctx, existing.ID, challengeType, title, items); err != nil {
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

// nextChallengeType 返回轮转序列中 last 之后的题型；last 为空或不在序列中时返回首个题型。
func nextChallengeType(last string) string {
	for i, t := range challengeCycle {
		if t == last {
			return challengeCycle[(i+1)%len(challengeCycle)]
		}
	}
	return challengeCycle[0]
}

// EnsureCount 保证同一 lang + level 至少有 n 个已发布关卡；不足时按题型轮转继续生成并直接发布。
// 题型从上一关的题型继续，保证序列沿 单词/搭配 → 语法 → 听力 循环；某题型素材不足时降级到下一可用题型。
// 用于学员解锁推进时预生成后续关卡，避免序列枯竭。day 写入生成当天，仅作信息字段。
func (b *Builder) EnsureCount(ctx context.Context, lang, level string, n int) error {
	existing, err := b.st.ListChallenges(ctx, lang, level)
	if err != nil {
		return err
	}
	count := len(existing)
	if count >= n {
		return nil
	}
	lastType := ""
	if count > 0 {
		lastType = existing[count-1].ChallengeType
	}
	dayStr := time.Now().UTC().Format("2006-01-02")
	for count < n {
		nextType := nextChallengeType(lastType)
		start := 0
		for i, t := range challengeCycle {
			if t == nextType {
				start = i
				break
			}
		}
		candidateTypes := make([]string, 0, len(challengeCycle))
		for offset := 0; offset < len(challengeCycle); offset++ {
			candidateTypes = append(candidateTypes, challengeCycle[(start+offset)%len(challengeCycle)])
		}
		var created *store.DailyChallenge
		built := false
		for _, ct := range candidateTypes {
			seed := fmt.Sprintf("%s|%s|%s|%d", dayStr, lang, level, count+1)
			items, buildErr := b.build(ctx, lang, level, seed, ct)
			if errors.Is(buildErr, ErrNoContent) {
				continue
			}
			if buildErr != nil {
				return buildErr
			}
			title := fmt.Sprintf("第%d关 %s %s关卡", count+1, level, typeLabel(ct))
			ch, _, err := b.st.CreateChallenge(ctx, store.DailyChallenge{
				Lang:          lang,
				Level:         level,
				Day:           dayStr,
				ChallengeType: ct,
				Title:         title,
				Status:        store.ChallengeStatusPublished,
			}, items)
			if err != nil {
				return err
			}
			created = ch
			built = true
			break
		}
		if !built {
			// 所有题型素材都不足，停止预生成，序列就此为止。
			return nil
		}
		lastType = created.ChallengeType
		count++
	}
	return nil
}

// EnsureCountAll 对所有已配置 (lang, level) 维护已发布关卡缓冲数量。
func (b *Builder) EnsureCountAll(ctx context.Context, n int) []error {
	targets, err := b.st.ChallengeTargets(ctx)
	if err != nil {
		return []error{err}
	}
	errs := make([]error, 0)
	for _, target := range targets {
		if err := b.EnsureCount(ctx, target[0], target[1], n); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", target[0], target[1], err))
		}
	}
	return errs
}

// build 按题型生成题目；素材不足返回 ErrNoContent。seed 用于稳定打散，保证同题复现。
func (b *Builder) build(ctx context.Context, lang, level, seed, challengeType string) ([]store.DailyChallengeItem, error) {
	var (
		items []store.DailyChallengeItem
		err   error
	)
	switch challengeType {
	case store.ChallengeTypeGrammar:
		items, err = b.buildGrammarItems(ctx, lang, level, seed)
	case store.ChallengeTypeListening:
		items, err = b.buildListeningItems(ctx, lang, level, seed)
	default:
		items, err = b.buildVocabItems(ctx, lang, level, seed)
	}
	if err != nil {
		return nil, err
	}
	if len(items) < 3 {
		return nil, ErrNoContent
	}
	if len(items) > itemsPerChallenge {
		items = items[:itemsPerChallenge]
	}
	for i := range items {
		items[i].Seq = i + 1
	}
	return items, nil
}

// buildVocabItems 生成单词 + 固定搭配共 6 题。
func (b *Builder) buildVocabItems(ctx context.Context, lang, level, seed string) ([]store.DailyChallengeItem, error) {
	wordItems, err := b.buildWordItems(ctx, lang, level, seed)
	if err != nil {
		return nil, err
	}
	collocationItems, err := b.buildCollocationItems(ctx, lang, level, seed)
	if err != nil {
		return nil, err
	}
	items := make([]store.DailyChallengeItem, 0, itemsPerChallenge)
	items = append(items, wordItems...)
	items = append(items, collocationItems...)
	return items, nil
}

func (b *Builder) buildWordItems(ctx context.Context, lang, level, seed string) ([]store.DailyChallengeItem, error) {
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

// buildGrammarItems 优先使用结构化语法题库，不足时用 rag_chunks 模板补足。
func (b *Builder) buildGrammarItems(ctx context.Context, lang, level, seed string) ([]store.DailyChallengeItem, error) {
	items, err := b.buildGrammarBankItems(ctx, lang, level, seed)
	if err != nil {
		return nil, err
	}
	if len(items) < itemsPerChallenge {
		fill, err := b.buildGrammarTemplateItems(ctx, lang, level, seed, itemsPerChallenge-len(items))
		if err != nil && !errors.Is(err, ErrNoContent) {
			return nil, err
		}
		items = append(items, fill...)
	}
	if len(items) == 0 {
		return nil, ErrNoContent
	}
	return items, nil
}

// buildGrammarBankItems 从 grammar_questions 取结构化题。
func (b *Builder) buildGrammarBankItems(ctx context.Context, lang, level, seed string) ([]store.DailyChallengeItem, error) {
	pool, err := b.st.ChallengeGrammarPool(ctx, lang, level, seed, itemsPerChallenge)
	if err != nil {
		return nil, err
	}
	items := make([]store.DailyChallengeItem, 0, len(pool))
	for _, q := range pool {
		var options []string
		if err := json.Unmarshal(q.Options, &options); err != nil {
			continue
		}
		answer := strings.TrimSpace(q.Answer)
		if len(options) < 2 || answer == "" {
			continue
		}
		if !containsFold(options, answer) {
			continue
		}
		question := q.Stem
		if question == "" {
			question = fmt.Sprintf("下面哪一项符合「%s」？", q.PointLabel)
		}
		item, err := newChoiceItem("grammar", fmt.Sprintf("grammar:%d", q.ID), question, options, answer, q.Explanation)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// buildGrammarTemplateItems 用语法切片里的示例句生成“哪句体现了该语法点”的题。
func (b *Builder) buildGrammarTemplateItems(ctx context.Context, lang, level, seed string, need int) ([]store.DailyChallengeItem, error) {
	if need <= 0 {
		return nil, nil
	}
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

	items := make([]store.DailyChallengeItem, 0, need)
	usedPrompts := make(map[string]bool)
	for _, point := range candidates {
		if len(items) >= need {
			break
		}
		correct := pickPrompt(point, seed)
		if correct == "" || usedPrompts[strings.ToLower(correct)] {
			continue
		}
		options := []string{correct}
		for _, other := range candidates {
			if len(options) >= optionsPerQuestion {
				break
			}
			distractor := pickPrompt(other, seed)
			if distractor == "" || strings.EqualFold(distractor, correct) || containsFold(options, distractor) {
				continue
			}
			options = append(options, distractor)
		}
		if len(options) < 2 {
			continue
		}
		usedPrompts[strings.ToLower(correct)] = true
		question := fmt.Sprintf("下面哪一句体现了「%s」？", point.Label)
		explanation := fmt.Sprintf("考点：%s。%s", point.Label, correct)
		item, err := newChoiceItem("grammar", "grammar:"+point.Key, question, options, correct, explanation)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func pickPrompt(point store.GrammarPoint, seed string) string {
	if len(point.Prompts) == 0 {
		return ""
	}
	index := len(hashHex(seed+point.Key+"|prompt")) % len(point.Prompts)
	for offset := 0; offset < len(point.Prompts); offset++ {
		prompt := strings.TrimSpace(point.Prompts[(index+offset)%len(point.Prompts)])
		if prompt != "" {
			return prompt
		}
	}
	return ""
}

// listeningManifest 是听力任务的 manifest.json 子集。
type listeningManifest struct {
	ExamID    string              `json:"exam_id"`
	Questions []listeningQuestion `json:"questions"`
}

type listeningQuestion struct {
	Number     int             `json:"number"`
	Start      float64         `json:"start"`
	End        float64         `json:"end"`
	Audio      string          `json:"audio"`
	Options    []string        `json:"options"`
	Answer     json.RawMessage `json:"answer"`
	Transcript string          `json:"transcript"`
}

type listeningCandidate struct {
	ExamID   string
	Number   int
	Audio    string
	Options  []string
	Answer   string
	Duration float64
	Text     string
}

// buildListeningItems 从已有听力任务的 manifest 中取题；优先同一套听力。
func (b *Builder) buildListeningItems(ctx context.Context, lang, level, seed string) ([]store.DailyChallengeItem, error) {
	jobs, err := b.st.ChallengeListeningJobs(ctx, lang, level)
	if err != nil {
		return nil, err
	}
	byExam := make(map[string][]listeningCandidate, len(jobs))
	for _, job := range jobs {
		manifest, err := b.readListeningManifest(job.ExamID)
		if err != nil {
			continue
		}
		candidates := listeningCandidates(job.ExamID, manifest)
		if len(candidates) > 0 {
			byExam[job.ExamID] = candidates
		}
	}
	if len(byExam) == 0 {
		return nil, ErrNoContent
	}

	examIDs := make([]string, 0, len(byExam))
	for examID := range byExam {
		examIDs = append(examIDs, examID)
	}
	sort.SliceStable(examIDs, func(i, j int) bool {
		return hashHex(seed+examIDs[i]) < hashHex(seed+examIDs[j])
	})

	// 优先使用单套听力凑满 6 题，保证同一次考试语境连贯。
	picked := make([]listeningCandidate, 0, itemsPerChallenge)
	for _, examID := range examIDs {
		if len(byExam[examID]) >= itemsPerChallenge {
			picked = append(picked, byExam[examID][:itemsPerChallenge]...)
			break
		}
	}
	if len(picked) < itemsPerChallenge {
		picked = picked[:0]
		for _, examID := range examIDs {
			candidates := append([]listeningCandidate(nil), byExam[examID]...)
			sort.SliceStable(candidates, func(i, j int) bool {
				return hashHex(fmt.Sprintf("%s|%s|%d", seed, examID, candidates[i].Number)) <
					hashHex(fmt.Sprintf("%s|%s|%d", seed, examID, candidates[j].Number))
			})
			for _, candidate := range candidates {
				if len(picked) >= itemsPerChallenge {
					break
				}
				picked = append(picked, candidate)
			}
			if len(picked) >= itemsPerChallenge {
				break
			}
		}
	}
	if len(picked) == 0 {
		return nil, ErrNoContent
	}

	items := make([]store.DailyChallengeItem, 0, len(picked))
	for _, candidate := range picked {
		refID := fmt.Sprintf("listening:%s:%d", candidate.ExamID, candidate.Number)
		question := fmt.Sprintf("听力第 %d 题", candidate.Number)
		item, err := newAudioItem(refID, question, candidate.Options, candidate.Answer, candidate.Audio, candidate.Duration, candidate.Text)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (b *Builder) readListeningManifest(examID string) (*listeningManifest, error) {
	if examID == "" {
		return nil, ErrNoContent
	}
	path := filepath.Join(b.workDir, "listening", examID, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest listeningManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// listeningCandidates 把 manifest 题目转成可出题的候选；丢弃无答案或无选项的题。
func listeningCandidates(examID string, manifest *listeningManifest) []listeningCandidate {
	if manifest == nil {
		return nil
	}
	out := make([]listeningCandidate, 0, len(manifest.Questions))
	for _, q := range manifest.Questions {
		audio := strings.TrimSpace(q.Audio)
		if q.Number <= 0 || audio == "" || strings.Contains(audio, "..") {
			continue
		}
		answerRaw := strings.TrimSpace(string(q.Answer))
		var answer string
		if err := json.Unmarshal(q.Answer, &answer); err != nil {
			answer = strings.Trim(answerRaw, `"`)
		}
		answer = strings.TrimSpace(answer)
		if answer == "" || answer == "null" {
			continue
		}

		options := make([]string, 0, len(q.Options))
		for _, opt := range q.Options {
			opt = strings.TrimSpace(opt)
			if opt != "" {
				options = append(options, opt)
			}
		}
		answer = normalizeListeningAnswer(answer, options)
		if answer == "" {
			continue
		}
		// 判断题固定用 Richtig/Falsch 两个选项，不采用 OCR 文本。
		if isTrueFalseAnswer(answer) {
			options = []string{"Richtig", "Falsch"}
		}
		if len(options) < 2 {
			continue
		}
		if !containsFold(options, answer) {
			continue
		}
		out = append(out, listeningCandidate{
			ExamID:   examID,
			Number:   q.Number,
			Audio:    filepath.ToSlash(filepath.Join("listening", examID, filepath.FromSlash(audio))),
			Options:  options,
			Answer:   answer,
			Duration: math.Round((q.End-q.Start)*10) / 10,
			Text:     strings.TrimSpace(q.Transcript),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

// normalizeListeningAnswer 把 A/B/C/D 映射为选项文本，判断题归一为 Richtig/Falsch。
func normalizeListeningAnswer(answer string, options []string) string {
	upper := strings.ToUpper(answer)
	switch upper {
	case "R", "RICHTIG", "TRUE", "T":
		return "Richtig"
	case "F", "FALSCH", "FALSE":
		return "Falsch"
	}
	if len(upper) == 1 && upper[0] >= 'A' && upper[0] <= 'D' {
		index := int(upper[0] - 'A')
		if index < len(options) {
			return options[index]
		}
	}
	for _, opt := range options {
		if strings.EqualFold(strings.TrimSpace(opt), answer) {
			return opt
		}
	}
	return ""
}

func isTrueFalseAnswer(answer string) bool {
	return answer == "Richtig" || answer == "Falsch"
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

func newAudioItem(refID, question string, options []string, correct, audio string, duration float64, explanation string) (store.DailyChallengeItem, error) {
	options = shuffleOptions(options, refID)
	prompt, err := json.Marshal(audioChoicePrompt{
		Type:     "audio_choice",
		Question: question,
		Options:  options,
		Audio:    audio,
		Duration: duration,
	})
	if err != nil {
		return store.DailyChallengeItem{}, err
	}
	answer, err := json.Marshal(choiceAnswer{Value: correct})
	if err != nil {
		return store.DailyChallengeItem{}, err
	}
	return store.DailyChallengeItem{
		Kind:        "listening",
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
