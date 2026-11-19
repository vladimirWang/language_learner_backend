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
	Title          string `json:"title"`
	Topic          string `json:"topic"`
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
	After    int      `json:"after_line"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Answer   int      `json:"answer"`
}

// listeningGenerate 同步生成 AI 对话听力任务：LLM 生成对话 + 选择题 → TTS 合成 → 写 manifest → 写 listening_jobs。
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

	ctx := c.Request.Context()
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	// 1. 取最近 5 天学习的新词
	since := h.now().AddDate(0, 0, -5)
	recentWords, err := h.store.RecentWords(ctx, req.DeviceID, req.Lang, since)
	if err != nil {
		h.logger.Printf("listening generate: RecentWords failed: %v", err)
		writeError(c, http.StatusInternalServerError, "load recent words failed")
		return
	}

	// 2. LLM 生成对话脚本
	dialogue, err := h.callGenerateDialogue(ctx, req.Lang, req.Level, req.Topic, recentWords, req.QuestionCount)
	if err != nil {
		h.logger.Printf("listening generate: LLM failed: %v", err)
		writeError(c, http.StatusInternalServerError, "llm generate failed: "+err.Error())
		return
	}
	if len(dialogue.Dialogue) == 0 {
		writeError(c, http.StatusInternalServerError, "llm returned empty dialogue")
		return
	}

	// 3. 构造 exam_id + 产物目录
	examID := fmt.Sprintf("ai-%s", time.Now().Format("20060102-150405"))
	if r, err := randHex(4); err == nil {
		examID += "-" + r
	}
	workRoot := os.Getenv("WORK_DIR")
	if workRoot == "" {
		workRoot = "work"
	}
	examDir := filepath.Join(workRoot, "listening", examID)
	audioDir := filepath.Join(examDir, "audio")
	if err := os.MkdirAll(audioDir, 0o755); err != nil {
		h.logger.Printf("listening generate: mkdir %s failed: %v", examDir, err)
		writeError(c, http.StatusInternalServerError, "prepare work dir failed")
		return
	}

	// 4. 创建 listening_jobs 记录
	jobID := "job-" + examID
	jobName := dialogue.Title
	if err := h.store.CreateListeningJob(ctx, jobID, examID, req.Lang, "ai-generated", true); err != nil {
		h.logger.Printf("listening generate: CreateListeningJob failed: %v", err)
		writeError(c, http.StatusInternalServerError, "create listening job failed")
		return
	}
	if err := h.store.StartListeningJob(ctx, jobID); err != nil {
		h.logger.Printf("listening generate: StartListeningJob failed: %v", err)
		writeError(c, http.StatusInternalServerError, "update listening job failed")
		return
	}
	_ = h.store.UpdateListeningJobMeta(ctx, jobID, &jobName, req.Level)

	h.store.AppendListeningLog(ctx, jobID, "llm", "info",
		fmt.Sprintf("生成对话 %d 轮 / 选择题 %d 道 / 参考新词 %d 个",
			len(dialogue.Dialogue), len(dialogue.Questions), len(recentWords)))

	// 5. TTS 合成每轮对话的音频
	type questionManifest struct {
		Number      int      `json:"number"`
		Clip        string   `json:"clip"`
		Line        int      `json:"line"`
		Transcript  string   `json:"transcript"`
		Speaker     string   `json:"speaker"`
		SpeakerName string   `json:"speaker_name"`
		Question    string   `json:"question,omitempty"`
		Options     []string `json:"options,omitempty"`
		Answer      int      `json:"answer,omitempty"`
	}

	questions := make([]questionManifest, 0, len(dialogue.Dialogue))
	for i, turn := range dialogue.Dialogue {
		speakerName := speakerByName(dialogue.Speakers, turn.Speaker)
		text := strings.TrimSpace(turn.Text)
		if text == "" {
			continue
		}
		clipName := fmt.Sprintf("q%02d.mp3", i+1)
		clipRel := "audio/" + clipName
		clipAbs := filepath.Join(audioDir, clipName)

		data, _, ttsErr := h.tts.Synthesize(ctx, text, req.Lang)
		qm := questionManifest{
			Number:      i + 1,
			Line:        turn.Line,
			Transcript:  text,
			Speaker:     turn.Speaker,
			SpeakerName: speakerName,
		}
		if ttsErr != nil {
			h.logger.Printf("listening generate: tts %d failed: %v", i+1, ttsErr)
			h.store.AppendListeningLog(ctx, jobID, "tts", "warn",
				fmt.Sprintf("第 %d 轮 TTS 失败，跳过音频", i+1))
		} else {
			if err := os.WriteFile(clipAbs, data, 0o644); err != nil {
				h.logger.Printf("listening generate: write %s failed: %v", clipAbs, err)
				_ = h.store.FailListeningJob(ctx, jobID, err.Error())
				writeError(c, http.StatusInternalServerError, "write audio failed")
				return
			}
			qm.Clip = clipRel
		}
		if q, ok := findQuestionForLine(dialogue.Questions, turn.Line); ok {
			qm.Question = q.Question
			qm.Options = q.Options
			qm.Answer = q.Answer
		}
		questions = append(questions, qm)
		if i%3 == 2 {
			h.store.AppendListeningLog(ctx, jobID, "tts", "info",
				fmt.Sprintf("TTS 进度 %d/%d", i+1, len(dialogue.Dialogue)))
		}
	}

	// 6. 组装 manifest.json
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
		h.logger.Printf("listening generate: marshal manifest failed: %v", err)
		_ = h.store.FailListeningJob(ctx, jobID, err.Error())
		writeError(c, http.StatusInternalServerError, "build manifest failed")
		return
	}
	manifestPath := filepath.Join(examDir, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		h.logger.Printf("listening generate: write manifest failed: %v", err)
		_ = h.store.FailListeningJob(ctx, jobID, err.Error())
		writeError(c, http.StatusInternalServerError, "write manifest failed")
		return
	}

	// 7. 预打包 zip
	zipPath := filepath.Join(examDir, examID+".zip")
	if err := buildZip(examDir, zipPath); err != nil {
		h.logger.Printf("listening generate: zip failed: %v", err)
		h.store.AppendListeningLog(ctx, jobID, "zip", "warn", "zip 打包失败（不影响主流程）")
	}

	// 8. 更新 listening_jobs 为 built
	if err := h.store.FinishListeningJob(ctx, jobID, "manifest.json"); err != nil {
		h.logger.Printf("listening generate: FinishListeningJob failed: %v", err)
		writeError(c, http.StatusInternalServerError, "finish listening job failed")
		return
	}
	h.store.AppendListeningLog(ctx, jobID, "done", "info",
		fmt.Sprintf("生成完成：exam_id=%s 题目=%d", examID, len(questions)))

	writeJSON(c, http.StatusOK, map[string]any{
		"job_id":          jobID,
		"exam_id":         examID,
		"title":           dialogue.Title,
		"speaker_count":   speakerCount,
		"dialogue_lines":  len(dialogue.Dialogue),
		"question_count":  len(questions),
		"vocabulary_used": dialogue.VocabularyUsed,
	})
}

// callGenerateDialogue 调 LLM 生成对话脚本。
func (h *Handler) callGenerateDialogue(ctx context.Context, lang, level, topic string, recentWords []store.RecentWord, qCount int) (*aiGenerateDialogue, error) {
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
现在需要生成一篇真实自然的 %s 双人小对话 + 选择题，供听力练习使用。

严格规则：
1. 语言必须是 %s，难度严格控制在 CEFR %s 级。禁止出现超出难度的生僻语法或词汇。
2. 角色 A 和 B 用自然的人名（不要用 A/B 指代）。
3. 对话共 8-12 轮，每轮 1-2 句，符合真实口语节奏。
4. 每道选择题必须针对该轮对话的事实内容，选项 3 个，答案唯一。不考主观推断。
5. 输出严格 JSON，不要任何 Markdown 代码块标记或额外说明文字。`, lang, lang, level)

	user := fmt.Sprintf(`难度：%s
目标：生成 8-12 轮双人对话 + %d 道事实型选择题
场景主题：%s
%s

请按以下 JSON schema 输出：
{
  "title": "...",
  "topic": "...",
  "speakers": [{"name": "...", "role": "A"}, {"name": "...", "role": "B"}],
  "dialogue": [
    {"line": 1, "speaker": "A", "text": "..."}
  ],
  "questions": [
    {"index": 1, "after_line": 3, "question": "...", "options": ["...", "...", "..."], "answer": 0}
  ],
  "vocabulary_used": ["..."]
}

questions.after_line 必须指向 dialogue 中某一条的 line 号；answer 是 options 的 0-index 下标。
vocabulary_used 列出你实际用到的本周新词（lemma 即可）。`,
		level, qCount, topicHint, recentWordText)

	raw, err := h.llm.ChatJSON(ctx, system, user, 1500)
	if err != nil {
		return nil, fmt.Errorf("llm ChatJSON: %w", err)
	}

	// 去掉可能的 Markdown 包裹
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

	// 规范化 dialogue.line
	for i := range result.Dialogue {
		if result.Dialogue[i].Line <= 0 {
			result.Dialogue[i].Line = i + 1
		}
	}
	maxLine := result.Dialogue[len(result.Dialogue)-1].Line
	for i := range result.Questions {
		if result.Questions[i].After <= 0 || result.Questions[i].After > maxLine {
			idx := (i + 1) * (len(result.Dialogue) / (len(result.Questions) + 1))
			if idx >= len(result.Dialogue) {
				idx = len(result.Dialogue) - 1
			}
			result.Questions[i].After = result.Dialogue[idx].Line
		}
		if len(result.Questions[i].Options) == 0 {
			result.Questions[i].Options = []string{"选项 A", "选项 B", "选项 C"}
			result.Questions[i].Answer = 0
		}
		if result.Questions[i].Answer < 0 || result.Questions[i].Answer >= len(result.Questions[i].Options) {
			result.Questions[i].Answer = 0
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

func findQuestionForLine(qs []aiQuestion, line int) (aiQuestion, bool) {
	for _, q := range qs {
		if q.After == line {
			return q, true
		}
	}
	return aiQuestion{}, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// buildZip 把 examDir 下所有文件预打包为 zipPath（真正的 ZIP 格式）。
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
			return nil // 跳过正在写的 zip 本身
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
