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
)

var challengeLevelPattern = regexp.MustCompile(`^(A[12]|B[12]|C[12]|N[1-5])$`)

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

// parseChallengeLang 校验学员端语种；当前缺省为德语。
func parseChallengeLang(c *gin.Context) (string, bool) {
	lang := strings.TrimSpace(c.Query("lang"))
	if lang == "" {
		lang = "de"
	}
	if !langPattern.MatchString(lang) {
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

// currentStreak 读取并按今天日期修正连续打卡天数（断档显示为 0）。
func (h *Handler) currentStreak(ctx context.Context, openid, lang string) store.UserStreak {
	streak, err := h.store.GetStreak(ctx, openid, lang)
	if err != nil {
		h.logger.Printf("daily challenge streak openid=%s: %v", openid, err)
		return store.UserStreak{Lang: lang}
	}
	return store.NormalizeStreak(streak, h.todayDay().Format("2006-01-02"))
}

// challengeItemView 是下发给客户端的题目（不含标准答案）。
type challengeItemView struct {
	Seq    int             `json:"seq"`
	Kind   string          `json:"kind"`
	Prompt json.RawMessage `json:"prompt"`
	Audio  *challengeAudio `json:"audio,omitempty"`
}

// challengeAudio 是听力题的短期签名播放地址。
type challengeAudio struct {
	URL      string  `json:"url"`
	Duration float64 `json:"duration"`
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
	workRoot := os.Getenv("WORK_DIR")
	if workRoot == "" {
		workRoot = "work"
	}
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

// dailyChallengeToday 返回当天关卡、当前用户的作答与连续打卡。
func (h *Handler) dailyChallengeToday(c *gin.Context) {
	user, ok := h.requireWXUser(c)
	if !ok {
		return
	}
	lang, ok := parseChallengeLang(c)
	if !ok {
		return
	}
	openid, level := user.OpenID, user.Level
	day := h.todayDay()
	resp := map[string]any{
		"date":        day.Format("2006-01-02"),
		"lang":        lang,
		"level":       level,
		"streak":      h.currentStreak(c.Request.Context(), openid, lang),
		"total_stars": user.TotalStars,
	}
	ch, items, err := h.challenge.EnsureDay(c.Request.Context(), lang, level, day, true)
	if err != nil {
		if errors.Is(err, challenge.ErrNoContent) {
			resp["challenge"] = nil
			resp["reason"] = "content_not_ready"
			writeJSON(c, http.StatusOK, resp)
			return
		}
		h.logger.Printf("daily challenge today lang=%s level=%s: %v", lang, level, err)
		writeError(c, http.StatusInternalServerError, "load daily challenge failed")
		return
	}

	if ch.Status != store.ChallengeStatusPublished {
		resp["challenge"] = nil
		resp["reason"] = "not_published"
		writeJSON(c, http.StatusOK, resp)
		return
	}

	views := make([]challengeItemView, 0, len(items))
	for _, item := range items {
		view := challengeItemView{Seq: item.Seq, Kind: item.Kind, Prompt: item.Prompt}
		if item.Kind == "listening" {
			var prompt struct {
				Audio    string  `json:"audio"`
				Duration float64 `json:"duration"`
			}
			if json.Unmarshal(item.Prompt, &prompt) == nil && prompt.Audio != "" {
				if url := h.challengeAudioURL(item.ID); url != "" {
					view.Audio = &challengeAudio{URL: url, Duration: prompt.Duration}
				}
			}
		}
		views = append(views, view)
	}
	resp["challenge"] = map[string]any{
		"id":             ch.ID,
		"day":            ch.Day,
		"challenge_type": ch.ChallengeType,
		"title":          ch.Title,
		"items":          views,
	}
	if attempt, err := h.store.GetAttempt(c.Request.Context(), openid, ch.ID); err == nil {
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
		h.logger.Printf("daily challenge attempt openid=%s: %v", openid, err)
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

// submitDailyChallenge 判分并保存最新作答；重复提交按历史最高星补差。
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

	ch, err := h.store.GetChallengeByID(c.Request.Context(), id)
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
	if ch.Day > h.todayDay().Format("2006-01-02") {
		writeError(c, http.StatusForbidden, "challenge not yet available")
		return
	}
	if ch.Level != user.Level {
		writeError(c, http.StatusForbidden, "challenge level does not match current user")
		return
	}
	items, err := h.store.ListChallengeItems(c.Request.Context(), id)
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

	submission, err := h.store.SubmitChallengeAttempt(c.Request.Context(), store.ChallengeAttempt{
		OpenID:      openid,
		ChallengeID: id,
		Answers:     rawAnswers,
		Score:       score,
		Total:       total,
		Stars:       stars,
	}, ch.Lang, ch.Day)
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
	streak := store.NormalizeStreak(submission.Streak, h.todayDay().Format("2006-01-02"))
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
		"streak":            streak,
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

// gradeChallenge 对比答案，返回得分与逐题结果。
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

// dailyChallengeCalendar 返回某月已发布关卡与当前用户的完成情况。
func (h *Handler) dailyChallengeCalendar(c *gin.Context) {
	user, ok := h.requireWXUser(c)
	if !ok {
		return
	}
	lang, ok := parseChallengeLang(c)
	if !ok {
		return
	}
	openid, level := user.OpenID, user.Level
	from, to, month, ok := h.parseChallengeMonth(c)
	if !ok {
		return
	}
	today := h.todayDay().Format("2006-01-02")
	if from > today {
		writeJSON(c, http.StatusOK, map[string]any{"month": month, "entries": []store.ChallengeCalendarEntry{}})
		return
	}
	if to > today {
		to = today
	}
	entries, err := h.store.ChallengeCalendar(c.Request.Context(), openid, lang, level, from, to)
	if err != nil {
		h.logger.Printf("daily challenge calendar openid=%s: %v", openid, err)
		writeError(c, http.StatusInternalServerError, "load calendar failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"month": month, "entries": entries})
}

// dailyChallengeStreak 返回当前用户某语种的连续打卡天数。
func (h *Handler) dailyChallengeStreak(c *gin.Context) {
	openid, ok := h.requireWXOpenID(c)
	if !ok {
		return
	}
	lang, ok := parseChallengeLang(c)
	if !ok {
		return
	}
	streak := h.currentStreak(c.Request.Context(), openid, lang)
	writeJSON(c, http.StatusOK, map[string]any{"streak": streak})
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
