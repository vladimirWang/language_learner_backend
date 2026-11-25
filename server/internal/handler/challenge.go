package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/challenge"
	"github.com/wangfernando/language-learner/server/internal/store"
	"github.com/wangfernando/language-learner/server/internal/streak"
)

var challengeLevelPattern = regexp.MustCompile(`^(A[12]|B[12]|C[12]|N[1-5])$`)

// unlockStars 是解锁下一关所需的最低星数。
const unlockStars = 2

// challengeStars 标准 6 题每 2 题 1 星；不足 6 题按正确率折算并执行 60% 向下取整兜底。
func challengeStars(score, total int) int {
	if total <= 0 || score <= 0 {
		return 0
	}
	stars := 0
	if total >= 6 {
		stars = score / 2
	} else {
		stars = score * 3 / total
		if score >= total*3/5 && stars < 1 {
			stars = 1
		}
	}
	if stars > 3 {
		return 3
	}
	return stars
}

// requireWXUser 从 Bearer token 解析并读取微信用户。
func (h *Handler) requireWXUser(c *gin.Context) (store.WXUser, bool) {
	if !h.wx.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "微信登录未配置")
		return store.WXUser{}, false
	}
	token := bearerToken(c)
	if token == "" {
		writeError(c, http.StatusUnauthorized, "unauthorized")
		return store.WXUser{}, false
	}
	uid, err := h.wx.VerifyToken(token, time.Now())
	if err != nil {
		writeError(c, http.StatusUnauthorized, "login expired")
		return store.WXUser{}, false
	}
	u, err := h.store.GetWXUserByID(c.Request.Context(), uid)
	if err != nil {
		writeError(c, http.StatusUnauthorized, "user not found")
		return store.WXUser{}, false
	}
	return u, true
}

// requireWXOpenID 从 Bearer token 解析微信用户 openid；未登录返回 401。
func (h *Handler) requireWXOpenID(c *gin.Context) (string, bool) {
	u, ok := h.requireWXUser(c)
	return u.OpenID, ok
}

// parseChallengeLang 校验学员端语种；未指定时使用服务端配置的缺省语种。
func (h *Handler) parseChallengeLang(c *gin.Context) (string, bool) {
	lang := strings.TrimSpace(c.Query("lang"))
	if lang == "" {
		lang = h.defaultLang
	}
	if !langPattern.MatchString(lang) || !h.supportedLangs[lang] {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return "", false
	}
	return lang, true
}

// todayDay 返回 APP_TZ 下的今天（只保留日期）。
func (h *Handler) todayDay() time.Time {
	now := h.now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// currentStreak 从 Redis bitmap 读取连续打卡；读取失败按零值降级。
func (h *Handler) currentStreak(ctx context.Context, openid, lang string) streak.Streak {
	out, err := h.streak.Get(ctx, openid, lang, h.todayDay().Format("2006-01-02"))
	if err != nil {
		h.logger.Printf("daily challenge streak openid=%s: %v", openid, err)
		return streak.Streak{Lang: lang}
	}
	return out
}

// challengeItemView 是下发给客户端的题目，附带标准答案与解析，供客户端本地即时判定。
type challengeItemView struct {
	Seq           int             `json:"seq"`
	Kind          string          `json:"kind"`
	Prompt        json.RawMessage `json:"prompt"`
	Audio         *challengeAudio `json:"audio,omitempty"`
	Image         *challengeImage `json:"image,omitempty"`
	CorrectAnswer string          `json:"correct_answer"`
	Explanation   string          `json:"explanation"`
}

// challengeAudio 是听力题的短期签名播放地址。
type challengeAudio struct {
	URL      string  `json:"url"`
	Duration float64 `json:"duration"`
}

// challengeImage 是听力题题面图片的短期签名地址。
type challengeImage struct {
	URL string `json:"url"`
}

// audioSecret 复用微信登录密钥签发短期音频链接。
func (h *Handler) audioSecret() string {
	if secret := strings.TrimSpace(h.wx.TokenSecret); secret != "" {
		return secret
	}
	return strings.TrimSpace(h.wx.AppSecret)
}

func (h *Handler) signChallengeAudio(itemID, exp int64) string {
	mac := hmac.New(sha256.New, []byte(h.audioSecret()))
	fmt.Fprintf(mac, "%d:%d", itemID, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// challengeAudioURL 生成 10 分钟有效的音频地址；未配置密钥时返回空串。
func (h *Handler) challengeAudioURL(itemID int64) string {
	if h.audioSecret() == "" || itemID <= 0 {
		return ""
	}
	exp := time.Now().Add(10 * time.Minute).Unix()
	return fmt.Sprintf("/api/levels/audio/%d?exp=%d&sig=%s", itemID, exp, h.signChallengeAudio(itemID, exp))
}

func (h *Handler) signChallengeImage(itemID, exp int64) string {
	mac := hmac.New(sha256.New, []byte(h.audioSecret()))
	fmt.Fprintf(mac, "image:%d:%d", itemID, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// challengeImageURL 生成 10 分钟有效的题面图片地址；未配置密钥时返回空串。
func (h *Handler) challengeImageURL(itemID int64) string {
	if h.audioSecret() == "" || itemID <= 0 {
		return ""
	}
	exp := time.Now().Add(10 * time.Minute).Unix()
	return fmt.Sprintf("/api/levels/image/%d?exp=%d&sig=%s", itemID, exp, h.signChallengeImage(itemID, exp))
}

// challengeWorkRoot 返回听力产物根目录（WORK_DIR）。
func challengeWorkRoot() string {
	root := os.Getenv("WORK_DIR")
	if root == "" {
		root = "work"
	}
	return root
}

// buildItemViews 把题目转成下发视图（含标准答案与解析；听力题附带短期签名音频地址）。
func (h *Handler) buildItemViews(items []store.DailyChallengeItem) []challengeItemView {
	views := make([]challengeItemView, 0, len(items))
	for _, item := range items {
		var ans struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal(item.Answer, &ans)
		view := challengeItemView{
			Seq:           item.Seq,
			Kind:          item.Kind,
			Prompt:        item.Prompt,
			CorrectAnswer: ans.Value,
			Explanation:   item.Explanation,
		}
		if item.Kind == "listening" {
			var prompt struct {
				Audio    string  `json:"audio"`
				Duration float64 `json:"duration"`
				Image    string  `json:"image"`
			}
			if json.Unmarshal(item.Prompt, &prompt) == nil {
				if prompt.Audio != "" {
					if url := h.challengeAudioURL(item.ID); url != "" {
						view.Audio = &challengeAudio{URL: url, Duration: prompt.Duration}
					}
				}
				image := strings.TrimSpace(prompt.Image)
				// 历史关卡落库时没有图片字段，按 ref_id 从 manifest 补图。
				if image == "" {
					image = challenge.ListeningImageForRef(challengeWorkRoot(), item.RefID)
				}
				if image != "" {
					if url := h.challengeImageURL(item.ID); url != "" {
						view.Image = &challengeImage{URL: url}
					}
				}
			}
		}
		views = append(views, view)
	}
	return views
}

// challengeAudioFile 校验签名后按 Range 返回听力切片。
func (h *Handler) challengeAudioFile(c *gin.Context) {
	if h.audioSecret() == "" {
		writeError(c, http.StatusServiceUnavailable, "音频服务未配置")
		return
	}
	itemID, err := strconv.ParseInt(c.Param("itemId"), 10, 64)
	if err != nil || itemID <= 0 {
		writeError(c, http.StatusBadRequest, "invalid item id")
		return
	}
	exp, err := strconv.ParseInt(c.Query("exp"), 10, 64)
	if err != nil || exp < time.Now().Unix() {
		writeError(c, http.StatusForbidden, "audio link expired")
		return
	}
	expected := h.signChallengeAudio(itemID, exp)
	if !hmac.Equal([]byte(c.Query("sig")), []byte(expected)) {
		writeError(c, http.StatusForbidden, "invalid audio signature")
		return
	}
	item, err := h.store.GetChallengeItem(c.Request.Context(), itemID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "item not found")
			return
		}
		h.logger.Printf("challenge audio item=%d: %v", itemID, err)
		writeError(c, http.StatusInternalServerError, "load item failed")
		return
	}
	if item.Kind != "listening" {
		writeError(c, http.StatusNotFound, "item has no audio")
		return
	}
	var prompt struct {
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(item.Prompt, &prompt); err != nil || strings.TrimSpace(prompt.Audio) == "" {
		writeError(c, http.StatusNotFound, "audio not found")
		return
	}
	workRoot := challengeWorkRoot()
	target, err := safeJoin(workRoot, prompt.Audio)
	if err != nil {
		writeError(c, http.StatusNotFound, "audio not found")
		return
	}
	if info, err := os.Stat(target); err != nil || info.IsDir() {
		writeError(c, http.StatusNotFound, "audio not found")
		return
	}
	c.Header("Content-Type", "audio/mp4")
	c.Header("Cache-Control", "private, max-age=600")
	http.ServeFile(c.Writer, c.Request, target)
}

// challengeImageFile 校验签名后返回听力题题面图片。
func (h *Handler) challengeImageFile(c *gin.Context) {
	if h.audioSecret() == "" {
		writeError(c, http.StatusServiceUnavailable, "图片服务未配置")
		return
	}
	itemID, err := strconv.ParseInt(c.Param("itemId"), 10, 64)
	if err != nil || itemID <= 0 {
		writeError(c, http.StatusBadRequest, "invalid item id")
		return
	}
	exp, err := strconv.ParseInt(c.Query("exp"), 10, 64)
	if err != nil || exp < time.Now().Unix() {
		writeError(c, http.StatusForbidden, "image link expired")
		return
	}
	expected := h.signChallengeImage(itemID, exp)
	if !hmac.Equal([]byte(c.Query("sig")), []byte(expected)) {
		writeError(c, http.StatusForbidden, "invalid image signature")
		return
	}
	item, err := h.store.GetChallengeItem(c.Request.Context(), itemID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "item not found")
			return
		}
		h.logger.Printf("challenge image item=%d: %v", itemID, err)
		writeError(c, http.StatusInternalServerError, "load item failed")
		return
	}
	if item.Kind != "listening" {
		writeError(c, http.StatusNotFound, "item has no image")
		return
	}
	var prompt struct {
		Image string `json:"image"`
	}
	if err := json.Unmarshal(item.Prompt, &prompt); err != nil {
		writeError(c, http.StatusNotFound, "image not found")
		return
	}
	image := strings.TrimSpace(prompt.Image)
	if image == "" {
		image = challenge.ListeningImageForRef(challengeWorkRoot(), item.RefID)
	}
	if image == "" {
		writeError(c, http.StatusNotFound, "image not found")
		return
	}
	target, err := safeJoin(challengeWorkRoot(), image)
	if err != nil {
		writeError(c, http.StatusNotFound, "image not found")
		return
	}
	if info, err := os.Stat(target); err != nil || info.IsDir() {
		writeError(c, http.StatusNotFound, "image not found")
		return
	}
	c.Header("Cache-Control", "private, max-age=600")
	http.ServeFile(c.Writer, c.Request, target)
}

// challengeView 是关卡序列里的单个关卡元数据（不含题目）。
type challengeView struct {
	ID            int64  `json:"id"`
	Position      int    `json:"position"`
	ChallengeType string `json:"challenge_type"`
	Title         string `json:"title"`
	ItemCount     int    `json:"item_count"`
	BestStars     int    `json:"best_stars"`
	Stars         int    `json:"stars"`
	Passed        bool   `json:"passed"`
	Unlocked      bool   `json:"unlocked"`
}

// buildChallengeViews 依据序列与用户作答计算每关的解锁状态，返回视图与已解锁关数。
// 解锁规则：第 0 关恒解锁；第 i 关解锁 ⟺ 第 i-1 关 best_stars>=2。
func buildChallengeViews(challenges []store.DailyChallenge, attempts map[int64]store.ChallengeAttempt) ([]challengeView, int) {
	views := make([]challengeView, 0, len(challenges))
	unlockedCount := 0
	prevUnlocked := true
	for i, ch := range challenges {
		unlocked := prevUnlocked
		if unlocked {
			unlockedCount = i + 1
		}
		var bestStars, stars int
		var passed bool
		if a, ok := attempts[ch.ID]; ok {
			bestStars = a.BestStars
			stars = a.Stars
			passed = a.Passed
		}
		views = append(views, challengeView{
			ID: ch.ID, Position: i + 1, ChallengeType: ch.ChallengeType,
			Title: ch.Title, ItemCount: ch.ItemCount,
			BestStars: bestStars, Stars: stars, Passed: passed, Unlocked: unlocked,
		})
		prevUnlocked = bestStars >= unlockStars
	}
	return views, unlockedCount
}

// listChallenges 返回当前用户在其等级下的关卡序列及解锁状态、通关进度与 streak。
func (h *Handler) listChallenges(c *gin.Context) {
	user, ok := h.requireWXUser(c)
	if !ok {
		return
	}
	lang, ok := h.parseChallengeLang(c)
	if !ok {
		return
	}
	openid, level := user.OpenID, user.Level
	ctx := c.Request.Context()

	loadViews := func() ([]challengeView, int, error) {
		challenges, err := h.store.ListChallenges(ctx, lang, level)
		if err != nil {
			return nil, 0, err
		}
		ids := make([]int64, len(challenges))
		for i, ch := range challenges {
			ids[i] = ch.ID
		}
		attempts, err := h.store.ListAttempts(ctx, openid, ids)
		if err != nil {
			return nil, 0, err
		}
		am := make(map[int64]store.ChallengeAttempt, len(attempts))
		for _, a := range attempts {
			am[a.ChallengeID] = a
		}
		views, unlocked := buildChallengeViews(challenges, am)
		return views, unlocked, nil
	}

	views, unlockedCount, err := loadViews()
	if err != nil {
		h.logger.Printf("list challenges lang=%s level=%s: %v", lang, level, err)
		writeError(c, http.StatusInternalServerError, "load challenges failed")
		return
	}

	// 保证缓冲：至少解锁前沿 + 2 关，让用户能看到下一关（锁定态）与后续缓冲。
	want := unlockedCount + 2
	if want < 5 {
		want = 5
	}
	if len(views) < want {
		if genErr := h.challenge.EnsureCount(ctx, lang, level, want); genErr != nil {
			h.logger.Printf("ensure challenge buffer lang=%s level=%s: %v", lang, level, genErr)
		} else if refreshed, _, relErr := loadViews(); relErr == nil {
			views = refreshed
		}
	}

	passedCount := 0
	currentPos := len(views)
	for i, v := range views {
		if v.Passed {
			passedCount++
		}
		if v.Unlocked && !v.Passed && currentPos == len(views) {
			currentPos = i + 1
		}
	}

	writeJSON(c, http.StatusOK, map[string]any{
		"lang":        lang,
		"level":       level,
		"total_stars": user.TotalStars,
		"streak":      h.currentStreak(ctx, openid, lang),
		"challenges":  views,
		"progress": map[string]any{
			"total":   len(views),
			"passed":  passedCount,
			"current": currentPos,
		},
	})
}

// challengePosition 返回某关卡在其序列中的 1-based 位置；不在已发布序列中返回 0。
func (h *Handler) challengePosition(ctx context.Context, ch store.DailyChallenge) int {
	challenges, err := h.store.ListChallenges(ctx, ch.Lang, ch.Level)
	if err != nil {
		return 0
	}
	for i, c := range challenges {
		if c.ID == ch.ID {
			return i + 1
		}
	}
	return 0
}

// isChallengeUnlocked 判断某关卡对当前用户是否解锁：序列首关恒解锁，其余需上一关 best_stars>=2。
func (h *Handler) isChallengeUnlocked(ctx context.Context, openid string, ch store.DailyChallenge) bool {
	challenges, err := h.store.ListChallenges(ctx, ch.Lang, ch.Level)
	if err != nil {
		return false
	}
	idx := -1
	for i, c := range challenges {
		if c.ID == ch.ID {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return idx == 0 // 不在序列中(idx<0)返回 false；首关(idx==0)返回 true
	}
	prev := challenges[idx-1]
	attempt, err := h.store.GetAttempt(ctx, openid, prev.ID)
	if err != nil {
		return false
	}
	return attempt.BestStars >= unlockStars
}

// getChallenge 返回单个关卡的题目（不含答案）、用户已答与逐题判分结果（仅已答题）、解锁态。
func (h *Handler) getChallenge(c *gin.Context) {
	user, ok := h.requireWXUser(c)
	if !ok {
		return
	}
	openid := user.OpenID
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid challenge id")
		return
	}
	ctx := c.Request.Context()
	ch, err := h.store.GetChallengeByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "challenge not found")
			return
		}
		h.logger.Printf("get challenge id=%d: %v", id, err)
		writeError(c, http.StatusInternalServerError, "load challenge failed")
		return
	}
	if ch.Status != store.ChallengeStatusPublished {
		writeError(c, http.StatusForbidden, "challenge not published")
		return
	}
	if ch.Level != user.Level {
		writeError(c, http.StatusForbidden, "challenge level does not match current user")
		return
	}
	if !h.isChallengeUnlocked(ctx, openid, *ch) {
		writeError(c, http.StatusForbidden, "challenge not unlocked")
		return
	}
	items, err := h.store.ListChallengeItems(ctx, id)
	if err != nil {
		h.logger.Printf("get challenge items id=%d: %v", id, err)
		writeError(c, http.StatusInternalServerError, "load challenge items failed")
		return
	}

	resp := map[string]any{
		"challenge": map[string]any{
			"id":             ch.ID,
			"position":       h.challengePosition(ctx, *ch),
			"challenge_type": ch.ChallengeType,
			"title":          ch.Title,
			"unlocked":       true,
		},
		"items":  h.buildItemViews(items),
		"streak": h.currentStreak(ctx, openid, ch.Lang),
	}

	attempt, err := h.store.GetAttempt(ctx, openid, id)
	if err == nil {
		resp["attempt"] = map[string]any{
			"answers":    attempt.Answers,
			"score":      attempt.Score,
			"total":      attempt.Total,
			"stars":      attempt.Stars,
			"best_stars": attempt.BestStars,
			"passed":     attempt.Passed,
			"checked_in": attempt.Passed,
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		h.logger.Printf("get challenge attempt openid=%s id=%d: %v", openid, id, err)
	}
	writeJSON(c, http.StatusOK, resp)
}

// submitAnswer 是客户端提交的单题答案（value 为所选选项文本）。
type submitAnswer struct {
	Seq   int    `json:"seq"`
	Value string `json:"value"`
}

type submitChallengeReq struct {
	Answers []submitAnswer `json:"answers"`
}

// submitDailyChallenge 判分并保存最新作答；重复提交按历史最高星补差；达标则标记当日打卡。
func (h *Handler) submitDailyChallenge(c *gin.Context) {
	user, ok := h.requireWXUser(c)
	if !ok {
		return
	}
	openid := user.OpenID
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid challenge id")
		return
	}
	var req submitChallengeReq
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	ctx := c.Request.Context()
	ch, err := h.store.GetChallengeByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "challenge not found")
			return
		}
		h.logger.Printf("submit challenge get id=%d: %v", id, err)
		writeError(c, http.StatusInternalServerError, "load challenge failed")
		return
	}
	if ch.Status != store.ChallengeStatusPublished {
		writeError(c, http.StatusForbidden, "challenge not published")
		return
	}
	// 关卡与日期解绑：不再校验 ch.Day 与今天的关系。
	if ch.Level != user.Level {
		writeError(c, http.StatusForbidden, "challenge level does not match current user")
		return
	}
	if !h.isChallengeUnlocked(ctx, openid, *ch) {
		writeError(c, http.StatusForbidden, "challenge not unlocked")
		return
	}
	items, err := h.store.ListChallengeItems(ctx, id)
	if err != nil {
		h.logger.Printf("submit challenge items id=%d: %v", id, err)
		writeError(c, http.StatusInternalServerError, "load challenge items failed")
		return
	}

	rawAnswers, err := json.Marshal(req.Answers)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid answers")
		return
	}
	score, itemResults := gradeChallenge(items, req.Answers)
	total := len(items)
	stars := challengeStars(score, total)

	submission, err := h.store.SubmitChallengeAttempt(ctx, store.ChallengeAttempt{
		OpenID:      openid,
		ChallengeID: id,
		Answers:     rawAnswers,
		Score:       score,
		Total:       total,
		Stars:       stars,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "challenge not found")
			return
		}
		h.logger.Printf("submit challenge save id=%d openid=%s: %v", id, openid, err)
		writeError(c, http.StatusInternalServerError, "save attempt failed")
		return
	}
	attempt := submission.Attempt
	// 打卡判定：本次提交后 best_stars>=2 即算当天打卡成功（幂等）；打卡日 = 提交当天，与关卡生成日无关。
	if attempt.Passed {
		day := h.todayDay().Format("2006-01-02")
		if err := h.streak.Mark(ctx, openid, ch.Lang, day); err != nil {
			h.logger.Printf("mark streak openid=%s day=%s: %v", openid, day, err)
		}
	}
	resp := map[string]any{
		"score":             attempt.Score,
		"total":             attempt.Total,
		"stars":             attempt.Stars,
		"best_stars":        attempt.BestStars,
		"stars_added":       submission.StarsAdded,
		"total_stars":       submission.TotalStars,
		"passed":            attempt.Passed,
		"checked_in":        attempt.Passed,
		"already_submitted": submission.AlreadySubmitted,
		"items":             itemResults,
		"streak":            h.currentStreak(ctx, openid, ch.Lang),
	}
	writeJSON(c, http.StatusOK, resp)
}

// challengeItemResult 是逐题判分结果。
type challengeItemResult struct {
	Seq           int    `json:"seq"`
	Kind          string `json:"kind"`
	Correct       bool   `json:"correct"`
	CorrectAnswer string `json:"correct_answer"`
	Explanation   string `json:"explanation"`
}

// gradeChallenge 对比答案，返回得分与逐题结果（含每题正确答案，用于结算页）。
func gradeChallenge(items []store.DailyChallengeItem, answers []submitAnswer) (int, []challengeItemResult) {
	selected := make(map[int]string, len(answers))
	for _, a := range answers {
		selected[a.Seq] = normalizeChallengeAnswer(a.Value)
	}
	score := 0
	results := make([]challengeItemResult, 0, len(items))
	for _, item := range items {
		var ans struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal(item.Answer, &ans)
		correct := selected[item.Seq] != "" && selected[item.Seq] == normalizeChallengeAnswer(ans.Value)
		if correct {
			score++
		}
		results = append(results, challengeItemResult{
			Seq:           item.Seq,
			Kind:          item.Kind,
			Correct:       correct,
			CorrectAnswer: ans.Value,
			Explanation:   item.Explanation,
		})
	}
	return score, results
}

func normalizeChallengeAnswer(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

// dailyChallengeCalendar 返回某月已打卡的日期列表（仅打卡圆点，不再关联关卡内容）。
func (h *Handler) dailyChallengeCalendar(c *gin.Context) {
	openid, ok := h.requireWXOpenID(c)
	if !ok {
		return
	}
	lang, ok := h.parseChallengeLang(c)
	if !ok {
		return
	}
	from, to, month, ok := h.parseChallengeMonth(c)
	if !ok {
		return
	}
	days, err := h.streak.MarkedDays(c.Request.Context(), openid, lang, from, to)
	if err != nil {
		h.logger.Printf("daily challenge calendar openid=%s: %v", openid, err)
		writeError(c, http.StatusInternalServerError, "load calendar failed")
		return
	}
	if days == nil {
		days = []string{}
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"month": month,
		"lang":  lang,
		"days":  days,
	})
}

// dailyChallengeStreak 返回当前用户某语种的连续打卡天数。
func (h *Handler) dailyChallengeStreak(c *gin.Context) {
	openid, ok := h.requireWXOpenID(c)
	if !ok {
		return
	}
	lang, ok := h.parseChallengeLang(c)
	if !ok {
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"streak": h.currentStreak(c.Request.Context(), openid, lang)})
}

// parseChallengeMonth 解析 ?month=YYYY-MM，缺省为当前月；返回当月首尾日期。
func (h *Handler) parseChallengeMonth(c *gin.Context) (string, string, string, bool) {
	now := h.now()
	month := strings.TrimSpace(c.Query("month"))
	if month == "" {
		month = now.Format("2006-01")
	}
	start, err := time.ParseInLocation("2006-01", month, now.Location())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid month, want YYYY-MM")
		return "", "", "", false
	}
	end := start.AddDate(0, 1, -1)
	return start.Format("2006-01-02"), end.Format("2006-01-02"), month, true
}
