package handler

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// aiGenerateDialogue 是 LLM 返回的对话 + 选择题结构化输出。
type aiGenerateDialogue struct {
	Title          string      `json:"title"`
	Topic          string      `json:"topic"`
	Speakers       []aiSpeaker `json:"speakers"`
	Dialogue       []aiTurn    `json:"dialogue"`
	Questions      []aiQuestion `json:"questions"`
	VocabularyUsed []string    `json:"vocabulary_used"`
}

type aiSpeaker struct {
	Name string `json:"name"`
	Role string `json:"role"` // "A" / "B"
}

type aiTurn struct {
	Line    int    `json:"line"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

type aiQuestion struct {
	Index    int      `json:"index"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Answer   int      `json:"answer"`
}

// listeningGenerate 异步启动 AI 对话听力生成任务，立即返回 job_id（HTTP 202）。
// 进度和最终结果通过 SSE /api/events?job_id=xxx 推送，或轮询 /api/listening/job/:jobId。
func (h *Handler) listeningGenerate(c *gin.Context) {
	if h.llm == nil || !h.llm.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "LLM not configured")
		return
	}
	if h.tts == nil || !h.tts.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "TTS not configured")
		return
	}

	var req struct {
		Lang          string `json:"lang"`
		Level         string `json:"level"`
		DeviceID      string `json:"device_id"`
		Topic         string `json:"topic"`
		QuestionCount int    `json:"question_count"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	req.Lang = strings.TrimSpace(req.Lang)
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Topic = strings.TrimSpace(req.Topic)
	if req.Lang == "" {
		req.Lang = "de"
	}
	if !langPattern.MatchString(req.Lang) {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}
	if !validVocabLevels[req.Level] {
		writeError(c, http.StatusBadRequest, "invalid level (A1-C2 / N5-N1)")
		return
	}
	if req.DeviceID == "" {
		req.DeviceID = "admin"
	}
	if req.QuestionCount <= 0 || req.QuestionCount > 10 {
		req.QuestionCount = 3
	}

	// 构造 exam_id + job_id
	examID := fmt.Sprintf("ai-%s", time.Now().Format("20060102-150405"))
	if r, err := randHex(4); err == nil {
		examID += "-" + r
	}
	jobID := "job-" + examID

	// 立即创建 listening_jobs 记录（pending）
	actx := c.Request.Context()
	if err := h.store.CreateListeningJob(actx, jobID, examID, req.Lang, "ai-generated", true); err != nil {
		h.logger.Printf("listening generate: CreateListeningJob failed: %v", err)
		writeError(c, http.StatusInternalServerError, "create listening job failed")
		return
	}

	// 立即返回 202 accepted —— 任务在 goroutine 里异步跑
	c.JSON(http.StatusAccepted, map[string]any{
		"job_id":         jobID,
		"exam_id":        examID,
		"status":         "pending",
		"message":        "任务已提交，通过 SSE /api/events?job_id=" + jobID + " 订阅进度",
		"sse_endpoint":   "/api/events?job_id=" + jobID,
		"detail_endpoint": "/api/listening/job/" + jobID,
	})

	// 起 goroutine 执行完整生成流程
	go h.runListeningGenerate(jobID, examID, struct {
		Lang          string
		Level         string
		DeviceID      string
		Topic         string
		QuestionCount int
	}{
		Lang:          req.Lang,
		Level:         req.Level,
		DeviceID:      req.DeviceID,
		Topic:         req.Topic,
		QuestionCount: req.QuestionCount,
	})
}

// runListeningGenerate 是异步 goroutine 主体。负责：SSE 推送 + 通知持久化 + 所有生成步骤。
func (h *Handler) runListeningGenerate(jobID, examID string, req struct {
	Lang          string
	Level         string
	DeviceID      string
	Topic         string
	QuestionCount int
}) {
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// 每个阶段的错误处理统一：失败 → 推 error SSE + FailListeningJob + InsertNotification(error)
	fail := func(stage, errMsg string, origErr error) {
		fullMsg := stage + ": " + errMsg
		if origErr != nil {
			fullMsg += " (" + origErr.Error() + ")"
		}
		h.logger.Printf("listening generate %s: %s", jobID, fullMsg)
		_ = h.store.FailListeningJob(ctx, jobID, fullMsg)
		_ = h.store.AppendListeningLog(ctx, jobID, stage, "error", fullMsg)
		globalSSEHub.Publish(sseEvent{
			Type:    "error",
			JobID:   jobID,
			Stage:   stage,
			Message: errMsg,
		})
		_, _ = h.store.InsertNotification(ctx, jobID, "error",
			fmt.Sprintf("AI 听力生成失败（%s）", req.Level), fullMsg,
			map[string]any{"exam_id": examID, "topic": req.Topic})
	}

	push := func(stage, level, msg string, data map[string]any) {
		_ = h.store.AppendListeningLog(ctx, jobID, stage, level, msg)
		globalSSEHub.Publish(sseEvent{
			Type:    "progress",
			JobID:   jobID,
			Stage:   stage,
			Level:   level,
			Message: msg,
			Data:    data,
		})
	}

	// 标记 running
	if err := h.store.StartListeningJob(ctx, jobID); err != nil {
		fail("start", "StartListeningJob failed", err)
		return
	}
	push("init", "info", "任务开始", nil)

	// 1. 取最近 5 天学习的新词
	since := h.now().AddDate(0, 0, -5)
	recentWords, err := h.store.RecentWords(ctx, req.DeviceID, req.Lang, since)
	if err != nil {
		fail("recent_words", "load recent words failed", err)
		return
	}
	push("llm", "info",
		fmt.Sprintf("参考本周新词 %d 个", len(recentWords)),
		map[string]any{"recent_count": len(recentWords)})

	// 2. LLM 生成对话脚本（对话每轮一道题，dialogue.length == questions.length）
	dialogue, err := h.callGenerateDialogue(ctx, req.Lang, req.Level, req.Topic, recentWords)
	if err != nil {
		fail("llm", "LLM 生成失败", err)
		return
	}
	if len(dialogue.Dialogue) == 0 {
		fail("llm", "LLM 返回空对话", nil)
		return
	}
	jobName := dialogue.Title
	_ = h.store.UpdateListeningJobMeta(ctx, jobID, &jobName, req.Level)
	push("llm", "info",
		fmt.Sprintf("LLM 生成完成：%d 轮对话 / %d 道选择题", len(dialogue.Dialogue), len(dialogue.Questions)),
		map[string]any{
			"title":           dialogue.Title,
			"dialogue_lines":  len(dialogue.Dialogue),
			"question_count":  len(dialogue.Questions),
			"vocabulary_used": dialogue.VocabularyUsed,
		})

	// 3. 构造产物目录
	workRoot := os.Getenv("WORK_DIR")
	if workRoot == "" {
		workRoot = "work"
	}
	examDir := filepath.Join(workRoot, "listening", examID)
	audioDir := filepath.Join(examDir, "audio")
	if err := os.MkdirAll(audioDir, 0o755); err != nil {
		fail("fs", "mkdir exam dir failed", err)
		return
	}

	// 4. TTS 合成每轮对话的音频
	// questionManifest 字段完全对齐 builder/listening.py 的产物结构，
	// 保证 client ListeningExamView 能无缝消费。
	type questionManifest struct {
		Number    int      `json:"number"`
		Start     float64  `json:"start"`
		End       float64  `json:"end"`
		Audio     string   `json:"audio,omitempty"`
		Transcript string  `json:"transcript"`
		Speakers  []string `json:"speakers"`
		Answer    *string  `json:"answer"`    // "A"/"B"/"C" 或 null（builder 里没答案时是 null）
		Options   []string `json:"options"`
		Question  string   `json:"question"`
	}

	// 把 LLM 返回的 questions 按 index 建 map，后续每轮对话按 index 查找
	qMap := make(map[int]aiQuestion, len(dialogue.Questions))
	for _, q := range dialogue.Questions {
		qMap[q.Index] = q
	}

	// 估算每轮对话的时间轴（builder 有 start/end，AI 没有音频源所以估一个）
	// 每轮约 3.5 秒，方便 client 展示；实际音频是独立文件，不依赖 start/end
	const secondsPerTurn = 3.5

	questions := make([]questionManifest, 0, len(dialogue.Dialogue))
	for i, turn := range dialogue.Dialogue {
		text := strings.TrimSpace(turn.Text)
		if text == "" {
			continue
		}
		// 角色字母 → 真实名字，speakers 数组只放真实名字
		speakerName := speakerByName(dialogue.Speakers, turn.Speaker)

		num := i + 1
		clipName := fmt.Sprintf("q%02d.mp3", num)
		clipRel := "audio/" + clipName
		clipAbs := filepath.Join(audioDir, clipName)

		// 估算 start/end（每轮约 3.5s），AI 无音频源所以用估算值
		startTime := float64(i) * secondsPerTurn
		endTime := startTime + secondsPerTurn

		data, _, ttsErr := h.tts.Synthesize(ctx, text, req.Lang)
		qm := questionManifest{
			Number:     num,
			Start:      round2(startTime),
			End:        round2(endTime),
			Transcript: text,
			Speakers:   []string{speakerName},
			Question:   "",
			Options:    []string{},
		}
		if ttsErr != nil {
			push("tts", "warn", fmt.Sprintf("第 %d 轮 TTS 失败，跳过音频", num), nil)
		} else {
			if err := os.WriteFile(clipAbs, data, 0o644); err != nil {
				fail("tts", "write audio failed", err)
				return
			}
			qm.Audio = clipRel
		}
		// 按 index 匹配 LLM 生成的问题（现在每轮都有配套题）
		if q, ok := qMap[turn.Line]; ok {
			qm.Question = q.Question
			if len(q.Options) > 0 {
				qm.Options = q.Options
				ans := answerLetter(q.Answer)
				if ans != "" {
					qm.Answer = &ans
				}
			}
		}
		questions = append(questions, qm)
		if i%3 == 2 {
			push("tts", "info",
				fmt.Sprintf("TTS 进度 %d/%d", num, len(dialogue.Dialogue)),
				map[string]any{"done": num, "total": len(dialogue.Dialogue)})
		}
	}

	// 5. 写 manifest.json
	speakerCount := len(dialogue.Speakers)
	if speakerCount < 2 {
		speakerCount = 2
	}
	manifest := map[string]any{
		"exam_id":         examID,
		"lang":            req.Lang,
		"title":           dialogue.Title,
		"source_url":      "ai-generated",
		"media_has_video": false,
		"media_file":      "",
		"model":           "llm-generated",
		"duration":        0,
		"speaker_count":   speakerCount,
		"is_dialogue":     true,
		"created_at":      h.now().Format(time.RFC3339),
		"question_count":  len(questions),
		"questions":       questions,
		"ai_generated":    true,
		"ai_meta": map[string]any{
			"level":           req.Level,
			"topic":           req.Topic,
			"vocabulary_used": dialogue.VocabularyUsed,
			"speaker_names":   extractSpeakerNames(dialogue.Speakers),
		},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fail("manifest", "marshal manifest failed", err)
		return
	}
	manifestPath := filepath.Join(examDir, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		fail("manifest", "write manifest failed", err)
		return
	}

	// 6. 预打包 zip
	zipPath := filepath.Join(examDir, examID+".zip")
	if err := buildZip(examDir, zipPath); err != nil {
		push("zip", "warn", "zip 打包失败（不影响主流程）", nil)
	}

	// 7. 标记任务完成
	if err := h.store.FinishListeningJob(ctx, jobID, "manifest.json"); err != nil {
		fail("finish", "FinishListeningJob failed", err)
		return
	}

	// 成功：SSE + 通知
	successData := map[string]any{
		"exam_id":         examID,
		"title":           dialogue.Title,
		"dialogue_lines":  len(dialogue.Dialogue),
		"question_count":  len(questions),
		"vocabulary_used": dialogue.VocabularyUsed,
	}
	globalSSEHub.Publish(sseEvent{
		Type:    "success",
		JobID:   jobID,
		Stage:   "done",
		Level:   "info",
		Message: fmt.Sprintf("生成完成：%s", dialogue.Title),
		Data:    successData,
	})
	_, _ = h.store.InsertNotification(ctx, jobID, "success",
		fmt.Sprintf("AI 听力生成完成：%s", dialogue.Title),
		fmt.Sprintf("%d 轮对话 / %d 道选择题 / 融入 %d 个新词",
			len(dialogue.Dialogue), len(questions), len(dialogue.VocabularyUsed)),
		map[string]any{
			"exam_id":         examID,
			"lang":            req.Lang,
			"level":           req.Level,
			"topic":           req.Topic,
			"question_count":  len(questions),
			"vocabulary_used": dialogue.VocabularyUsed,
		})
}

// callGenerateDialogue 调 LLM 生成对话脚本。
// 强制 LLM 每一轮对话都配套一道事实型选择题（dialogue.length == questions.length）。
func (h *Handler) callGenerateDialogue(ctx context.Context, lang, level, topic string, recentWords []store.RecentWord) (*aiGenerateDialogue, error) {
	var recentWordText string
	if len(recentWords) > 0 {
		recentWordText = "本周已学词汇（优先融入，至少覆盖 60%）：\n"
		for i, w := range recentWords {
			if i >= 30 {
				recentWordText += fmt.Sprintf("...（共 %d 个词，截断前 30 个）\n", len(recentWords))
				break
			}
			recentWordText += fmt.Sprintf("- %s (%s): %s\n", w.Lemma, w.Level, w.ChineseMean)
		}
	} else {
		recentWordText = "（暂无本周已学词汇，请自选符合难度的日常词汇）\n"
	}

	topicHint := topic
	if topicHint == "" {
		topicHint = "自由发挥一个贴近日常生活的场景（购物、问路、自我介绍、咖啡馆点单、打车、看病等）"
	}

	system := fmt.Sprintf(`你是一位严谨的 CEFR 外语教学助手。
现在需要生成一篇真实自然的 %s 双人小对话 + 配套选择题，供听力练习使用。

严格规则：
1. 语言必须是 %s，难度严格控制在 CEFR %s 级。禁止出现超出难度的生僻语法或词汇。
2. 角色 A 和 B 用自然的人名（不要用 A/B 指代）。
3. 对话共 8-12 轮，每轮 1-2 句，符合真实口语节奏。
4. 每一轮对话都必须配套一道选择题（questions 数组长度必须等于 dialogue 长度），题号与对话轮次一一对应。

【出题铁律 — 违反即视为错误】
5. 每道题的正确答案**必须能在 dialogue[line=该题index] 或其前几轮里直接找到文字证据**。
   - 可以问：该轮谁说了什么、做了什么、为什么、在哪里、几点、多少钱、几个等事实型问题。
   - 题干里直接用人名（Tom/Anna），**严禁**写 "Sprecher A" / "Sprecher B" / "角色A" 这种占位符。
   - 严禁问：对话里完全没提到的信息（如品牌偏好、未出现的人名、后续情节、推测意图）。
   - 严禁正确答案需要跨越多轮复杂推理。
6. 选项 3 个，正确答案的文字片段必须出现在 dialogue 文本里，干扰项不能出现在任何 dialogue 里。
7. 输出严格 JSON，不要任何 Markdown 代码块标记或额外说明文字。`, lang, lang, level)

	user := fmt.Sprintf(`难度：%s
目标：生成 8-12 轮双人对话 + 每轮配套一道选择题（dialogue 和 questions 长度必须相等）
场景主题：%s
%s

示例（仅供参考格式，内容需完全不同）：
dialogue[line=2] = {"line":2,"speaker":"B","text":"Ich brauche neue Schuhe. Die alten sind kaputt."}
question[index=2] = {"index":2,"question":"Warum braucht Tom neue Schuhe?","options":["Weil sie zu klein sind.","Weil sie kaputt sind.","Weil sie ihm nicht gefallen."],"answer":1}
→ 正确答案 "kaputt" 直接出现在 line=2 的对话文本里。题干里直接用角色真实名字（Tom/Anna），不要用 "Sprecher A/B" 这种角色字母。

请按以下 JSON schema 输出：
{
  "title": "...",
  "topic": "...",
  "speakers": [{"name": "...", "role": "A"}, {"name": "...", "role": "B"}],
  "dialogue": [
    {"line": 1, "speaker": "A", "text": "..."}
  ],
  "questions": [
    {"index": 1, "question": "...", "options": ["...", "...", "..."], "answer": 0}
  ],
  "vocabulary_used": ["..."]
}

questions.index 必须和 dialogue.line 一一对应（index=1 对应 line=1）。
answer 是 options 的 0-index 下标。
vocabulary_used 列出你实际用到的本周新词（lemma 即可）。`,
		level, topicHint, recentWordText)

	raw, err := h.llm.ChatJSON(ctx, system, user, 2000)
	if err != nil {
		return nil, fmt.Errorf("llm ChatJSON: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result aiGenerateDialogue
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("llm response not valid JSON: %w\nraw: %s", err, truncate(raw, 500))
	}
	if len(result.Dialogue) == 0 {
		return nil, fmt.Errorf("llm returned empty dialogue, raw=%s", truncate(raw, 300))
	}

	for i := range result.Dialogue {
		if result.Dialogue[i].Line <= 0 {
			result.Dialogue[i].Line = i + 1
		}
	}
	// 兜底：保证每道题都合法，index 从 1 开始递增
	for i := range result.Questions {
		if result.Questions[i].Index <= 0 {
			result.Questions[i].Index = i + 1
		}
		if len(result.Questions[i].Options) == 0 {
			result.Questions[i].Options = []string{"选项 A", "选项 B", "选项 C"}
			result.Questions[i].Answer = 0
		}
		if result.Questions[i].Answer < 0 || result.Questions[i].Answer >= len(result.Questions[i].Options) {
			result.Questions[i].Answer = 0
		}
	}
	// 兜底：如果 LLM 没给每轮都出题（questions 长度 < dialogue），为缺失轮次补空题
	if len(result.Questions) < len(result.Dialogue) {
		existingIdx := make(map[int]bool, len(result.Questions))
		for _, q := range result.Questions {
			existingIdx[q.Index] = true
		}
		for _, turn := range result.Dialogue {
			if !existingIdx[turn.Line] {
				result.Questions = append(result.Questions, aiQuestion{
					Index:    turn.Line,
					Question: "",
					Options:  []string{},
					Answer:   0,
				})
			}
		}
	}

	// 兜底校验：每道题正确答案的文字片段必须在对应 dialogue 轮次里能找到。
	// LLM 偶尔会出"问对话里没提到的信息"（如品牌偏好），这里做简单字符串匹配检查。
	dialogueByLine := make(map[int]string, len(result.Dialogue))
	for _, t := range result.Dialogue {
		dialogueByLine[t.Line] = t.Text
	}
	for i := range result.Questions {
		q := &result.Questions[i]
		if q.Answer < 0 || q.Answer >= len(q.Options) {
			continue
		}
		correctOpt := q.Options[q.Answer]
		// 在对应轮次 + 前一轮里找（因为信息可能在前一轮铺垫）
		hitText := dialogueByLine[q.Index] + " " + dialogueByLine[q.Index-1]
		if !answerTextHintPresent(correctOpt, hitText) {
			// 找不到 → 用该轮 dialogue 里的关键短语作为正确答案，重新生成 3 个选项
			if turnText, ok := dialogueByLine[q.Index]; ok {
				phrases := extractKeyPhrases(turnText)
				if len(phrases) >= 3 {
					q.Options = phrases
					q.Answer = 0
					speakerRole := speakerForLine(result.Dialogue, q.Index)
					speakerName := speakerByName(result.Speakers, speakerRole)
					q.Question = fmt.Sprintf("Was sagt %s?", speakerName)
				}
			}
		}
	}

	return &result, nil
}

func speakerByName(speakers []aiSpeaker, role string) string {
	for _, s := range speakers {
		if strings.EqualFold(s.Role, role) {
			return s.Name
		}
	}
	return role
}

func extractSpeakerNames(speakers []aiSpeaker) []string {
	out := make([]string, 0, len(speakers))
	for _, s := range speakers {
		out = append(out, s.Name)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// answerTextHintPresent 检查正确答案的关键词是否真的出现在 dialogue 文本里。
// 把答案拆成每个词（至少 3 字符），只要有一个出现在 dialogue 里就算命中。
func answerTextHintPresent(answerOpt, dialogueText string) bool {
	words := strings.FieldsFunc(answerOpt, func(r rune) bool {
		return r == '.' || r == '?' || r == '!' || r == ',' || r == '"'
	})
	dialogueLower := strings.ToLower(dialogueText)
	for _, w := range words {
		w = strings.TrimSpace(w)
		if len(w) >= 3 && strings.Contains(dialogueLower, strings.ToLower(w)) {
			return true
		}
	}
	// 兜底：直接整句检查
	return strings.Contains(strings.ToLower(dialogueText), strings.ToLower(strings.TrimSpace(answerOpt)))
}

// extractKeyPhrases 从单句 dialogue 里拆出 3 个候选短语（粗暴版本）。
// 用逗号/连接词切分，取有意义的片段。
func extractKeyPhrases(text string) []string {
	text = strings.TrimSpace(text)
	var parts []string
	for _, seg := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == '.' || r == '!' || r == '?' || r == ';'
	}) {
		seg = strings.TrimSpace(seg)
		if len(seg) > 0 {
			parts = append(parts, seg)
		}
	}
	// 如果切出来不足 3 段，再按 "und" / "oder" / "aber" 切
	if len(parts) < 3 {
		var more []string
		for _, p := range parts {
			for _, seg := range strings.Split(p, " und ") {
				more = append(more, strings.TrimSpace(seg))
			}
		}
		parts = more
	}
	if len(parts) < 3 {
		var more []string
		for _, p := range parts {
			for _, seg := range strings.Split(p, " oder ") {
				more = append(more, strings.TrimSpace(seg))
			}
		}
		parts = more
	}
	// 去空
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// speakerForLine 返回指定轮次的说话人角色（A/B）。
func speakerForLine(dialogue []aiTurn, line int) string {
	for _, t := range dialogue {
		if t.Line == line {
			return t.Speaker
		}
	}
	return "?"
}

func answerLetter(idx int) string {
	if idx < 0 || idx > 25 {
		return ""
	}
	return string(rune('A' + idx))
}

// round2 把 float 保留两位小数（匹配 builder 的 round(x, 2)）。
func round2(x float64) float64 {
	return float64(int64(x*100+0.5)) / 100
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func buildZip(examDir, zipPath string) error {
	tmp := zipPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		os.Remove(tmp)
	}()
	zw := zip.NewWriter(f)
	defer zw.Close()

	baseName := filepath.Base(examDir)
	return filepath.Walk(examDir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() {
			return werr
		}
		rel, err := filepath.Rel(examDir, path)
		if err != nil {
			return err
		}
		if rel == filepath.Base(zipPath) {
			return nil
		}
		zwf, err := zw.Create(filepath.Join(baseName, rel))
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(zwf, src)
		return err
	})
}

// listeningEvents 是 SSE 订阅端点：GET /api/events?job_id=xxx。
// 订阅后直到 HTTP 连接断开或 job 完成（success/error），期间推送所有 progress 事件。
// 每隔 25 秒发一次 keepalive 防止代理/浏览器断连。
func (h *Handler) listeningEvents(c *gin.Context) {
	jobID := strings.TrimSpace(c.Query("job_id"))
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "missing job_id")
		return
	}

	ch, release := globalSSEHub.Subscribe(jobID)
	defer release()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	// 立即发一条 connect 事件
	_, _ = c.Writer.Write([]byte(formatSSE(sseEvent{
		Type:    "connected",
		JobID:   jobID,
		Message: "SSE connected",
	})))
	c.Writer.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-keepalive.C:
			_, _ = c.Writer.Write([]byte(": keepalive\n\n"))
			c.Writer.Flush()
		case evt, ok := <-ch:
			if !ok {
				return
			}
			_, _ = c.Writer.Write([]byte(formatSSE(evt)))
			c.Writer.Flush()
			// success / error 事件后连接客户端自己断开；我们也主动关
			if evt.Type == "success" || evt.Type == "error" {
				return
			}
		}
	}
}

// --- 听力解析任务状态通知 ---

// listeningStageLabel 把 builder 输出的阶段标记映射为中文状态与通知类型。
func listeningStageLabel(stage string) (label, eventType string) {
	switch stage {
	case "queued":
		return "未下载", "info"
	case "downloading":
		return "下载中", "progress"
	case "downloaded":
		return "已下载", "progress"
	case "parsing_pending":
		return "未开始解析", "progress"
	case "parsing":
		return "解析中", "progress"
	case "completed":
		return "已完成", "success"
	case "failed":
		return "解析失败", "error"
	default:
		return stage, "info"
	}
}

// onListeningStage 由 scheduler 回调：同一个 job 只保留一条通知（原地更新），
// 并通过全局 SSE 推给管理台消息列表。
func (h *Handler) onListeningStage(jobID, examID, stage, message string) {
	label, eventType := listeningStageLabel(stage)
	if message == "" {
		message = label
	}
	title := "听力解析 · " + label
	payload := map[string]any{
		"exam_id": examID,
		"stage":   stage,
	}
	ctx := context.Background()
	id, err := h.store.UpsertNotificationByJob(ctx, jobID, eventType, title, message, payload)
	if err != nil {
		h.logger.Printf("upsert listening notification %s: %v", jobID, err)
	}
	unread, _ := h.store.CountUnreadNotifications(ctx)
	globalSSEHub.PublishAll(sseEvent{
		Type:    "notification",
		JobID:   jobID,
		Stage:   stage,
		Level:   eventType,
		Message: message,
		Data: map[string]any{
			"notification": map[string]any{
				"id":         id,
				"job_id":     jobID,
				"event_type": eventType,
				"title":      title,
				"message":    message,
				"payload":    payload,
				"created_at": time.Now().Format(time.RFC3339),
				"read_at":    nil,
			},
			"unread_count": unread,
		},
	})
}

// notificationEvents 是管理台消息中心的全局 SSE 端点：只推送任务状态变更通知。
// 每隔 25 秒发一次 keepalive 防止代理/浏览器断连。
func (h *Handler) notificationEvents(c *gin.Context) {
	ch, release := globalSSEHub.SubscribeAll()
	defer release()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	_, _ = c.Writer.Write([]byte(formatSSE(sseEvent{
		Type:    "connected",
		Message: "notification stream connected",
	})))
	c.Writer.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-keepalive.C:
			_, _ = c.Writer.Write([]byte(": keepalive\n\n"))
			c.Writer.Flush()
		case evt, ok := <-ch:
			if !ok {
				return
			}
			_, _ = c.Writer.Write([]byte(formatSSE(evt)))
			c.Writer.Flush()
		}
	}
}

// --- 系统通知 CRUD ---

func (h *Handler) listNotifications(c *gin.Context) {
	limit := 50
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &limit); n == 1 && limit > 0 && limit <= 500 && err == nil {
			// ok
		} else {
			limit = 50
		}
	}
	ctx := c.Request.Context()
	items, err := h.store.ListNotifications(ctx, limit)
	if err != nil {
		h.logger.Printf("list notifications: %v", err)
		writeError(c, http.StatusInternalServerError, "load notifications failed")
		return
	}
	unread, _ := h.store.CountUnreadNotifications(ctx)

	type notifOut struct {
		ID        int64          `json:"id"`
		JobID     string         `json:"job_id"`
		EventType string         `json:"event_type"`
		Title     string         `json:"title"`
		Message   string         `json:"message"`
		Payload   map[string]any `json:"payload"`
		CreatedAt string         `json:"created_at"`
		ReadAt    *string        `json:"read_at"`
	}
	out := make([]notifOut, 0, len(items))
	for _, it := range items {
		n := notifOut{
			ID:        it.ID,
			JobID:     it.JobID,
			EventType: it.EventType,
			Title:     it.Title,
			Message:   it.Message,
			Payload:   it.NotificationPayload(),
			CreatedAt: it.CreatedAt.Format(time.RFC3339),
		}
		if it.ReadAt != nil {
			s := it.ReadAt.Format(time.RFC3339)
			n.ReadAt = &s
		}
		out = append(out, n)
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"notifications": out,
		"unread_count":  unread,
	})
}

func (h *Handler) markNotificationRead(c *gin.Context) {
	var req struct {
		ID    *int64 `json:"id"`
		All   bool   `json:"all"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json")
		return
	}
	ctx := c.Request.Context()
	if req.All {
		n, err := h.store.MarkAllNotificationsRead(ctx)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(c, http.StatusOK, map[string]any{"marked": n})
		return
	}
	if req.ID == nil {
		writeError(c, http.StatusBadRequest, "missing id or all=true")
		return
	}
	ok, err := h.store.MarkNotificationRead(ctx, *req.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": ok})
}
