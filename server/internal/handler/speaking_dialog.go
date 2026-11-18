package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

// speakingReplyReq 是「受控追问 / 搭档回应」的请求体。
type speakingReplyReq struct {
	Lang          string            `json:"lang"`
	Level         string            `json:"level"`
	ExamPart      string            `json:"exam_part"`
	Topic         string            `json:"topic"`
	Role          string            `json:"role"` // examiner / partner
	History       []speakingTurnReq `json:"history"`
	FollowupCount int               `json:"followup_count"`
	PromptDE      string            `json:"prompt_de"`
}

type speakingTurnReq struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type speakingReplyResp struct {
	Reply  string `json:"reply"`
	Source string `json:"source"` // llm / template
}

var cjkRe = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}]`)

// 各等级单句允许的最大词数，用于拦截 LLM 超纲长句。
var levelMaxWords = map[string]int{
	"A1": 12, "A2": 16, "B1": 22, "B2": 28, "C1": 32, "C2": 36,
}

// 追问模板：LLM 不可用或输出不合规时使用，保证无幻觉。
var examinerTemplates = map[string][]string{
	"part1_intro": {
		"Können Sie das bitte noch etwas genauer sagen?",
		"Und warum ist das für Sie wichtig?",
		"Wie oft machen Sie das?",
		"Haben Sie ein Beispiel dafür?",
		"Und wie war das früher bei Ihnen?",
	},
	"part2_presentation": {
		"Was ist dabei der wichtigste Punkt für Sie?",
		"Können Sie dazu ein Beispiel geben?",
		"Welche Erfahrung haben Sie damit gemacht?",
		"Was würden Sie anderen in dieser Situation empfehlen?",
		"Gibt es dabei auch Nachteile?",
	},
	"part3_discussion": {
		"Das ist ein guter Punkt. Wie sehen Sie das?",
		"Können Sie das noch etwas erklären?",
		"Was würden Sie in dieser Situation machen?",
		"Ich sehe das etwas anders. Was meinen Sie dazu?",
		"Und wie wichtig ist Ihnen das persönlich?",
	},
}

var partnerTemplates = []string{
	"Das sehe ich ähnlich. Aber ich finde, es gibt auch Nachteile.",
	"Interessant. Bei mir ist das ein bisschen anders.",
	"Guter Punkt. Was würden Sie in dieser Situation machen?",
	"Ich stimme Ihnen teilweise zu. Wie wichtig ist das für Sie?",
	"Das kann ich gut verstehen. Haben Sie ein Beispiel dafür?",
	"Da bin ich anderer Meinung. Wie würden Sie das begründen?",
}

// normalizeText 去掉标点与多余空白并转小写，用于判重。
func normalizeText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer(
		".", "", "?", "", "!", "", ",", "", ";", "", ":", "",
		"\"", "", "'", "", "„", "", "“", "", "”", "",
	)
	return strings.Join(strings.Fields(replacer.Replace(text)), " ")
}

// pickTemplate 在模板池中轮转，优先选择本轮对话里没出现过的句子。
func pickTemplate(part, role string, followupCount int, used map[string]bool) string {
	pool := partnerTemplates
	if role != "partner" {
		if p, ok := examinerTemplates[part]; ok && len(p) > 0 {
			pool = p
		} else {
			pool = examinerTemplates["part1_intro"]
		}
	}
	if len(pool) == 0 {
		return "Können Sie das noch etwas genauer erklären?"
	}
	start := followupCount % len(pool)
	for i := 0; i < len(pool); i++ {
		candidate := pool[(start+i)%len(pool)]
		if !used[normalizeText(candidate)] {
			return candidate
		}
	}
	return pool[start]
}

func examinerSystemPrompt(level, topic string) string {
	return fmt.Sprintf(`你是歌德学院 %s 级别口语考试的考官，与考生进行德语对话。

必须做到：
1. 只使用 %s 等级范围内的词汇与句式，禁止超纲。
2. 每轮只输出一到两句德语提问，围绕话题 %s，不要跳到其他话题。
3. 不做语法讲解，只做自然的追问与引导。
4. 不评价对错、不打分。
5. 全程德语，不使用英语或中文。
6. 不要重复对话记录里已经问过的问题，每次追问都要换一个角度。
7. 只输出德语文本本身，不要加引号、翻译或解释。`, level, level, topic)
}

func partnerSystemPrompt(level, topic string) string {
	return fmt.Sprintf(`你是与考生同等级的 %s 德语考生，正在参加歌德口语第三部分的双人讨论，话题为 %s。

必须做到：
1. 以同级考生口吻表达观点，句子长度与词汇难度和 %s 一致。
2. 有观点、有互动：可以附和、补充或温和反驳，但不主导讨论。
3. 不纠正对方语法，不做讲解，不给分数。
4. 单轮输出不超过两句。
5. 不要重复对话记录里已经说过的话，每次回应都要有新观点。
6. 全程德语，只围绕 %s。只输出德语文本本身，不要加引号或解释。`, level, topic, level, topic)
}

// sanitizeReply 去掉引号、换行与多余空白的干扰。
func sanitizeReply(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "“”\"'`")
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.Join(strings.Fields(text), " ")
}

// validGermanReply 做轻量后置校验：命中异常则拒绝该输出并回退模板。
func validGermanReply(text, level string) bool {
	if text == "" {
		return false
	}
	if cjkRe.MatchString(text) {
		return false
	}
	n := utf8.RuneCountInString(text)
	if n < 5 || n > 240 {
		return false
	}
	// 句数不超过两句
	if strings.Count(text, ".")+strings.Count(text, "?")+strings.Count(text, "!") > 2 {
		return false
	}
	maxWords := levelMaxWords[level]
	if maxWords == 0 {
		maxWords = 22
	}
	words := strings.Fields(text)
	if len(words) > maxWords*2 {
		return false
	}
	for _, sentence := range regexp.MustCompile(`[.!?]+`).Split(text, -1) {
		fields := strings.Fields(sentence)
		if len(fields) > maxWords {
			return false
		}
	}
	return true
}

// speakingReply 生成追问或搭档回应：优先 LLM，异常时回退模板。
func (h *Handler) speakingReply(w http.ResponseWriter, r *http.Request) {
	var req speakingReplyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	req.ExamPart = strings.TrimSpace(req.ExamPart)
	req.Topic = strings.TrimSpace(req.Topic)
	req.Role = strings.TrimSpace(req.Role)
	if !validCefrLevels[req.Level] {
		writeError(w, http.StatusBadRequest, "invalid level")
		return
	}
	if !validExamParts[req.ExamPart] {
		writeError(w, http.StatusBadRequest, "invalid exam_part")
		return
	}
	if req.Role != "partner" {
		req.Role = "examiner"
	}
	if req.FollowupCount < 0 {
		req.FollowupCount = 0
	}

	// 已出现过的句子：用于避免重复提问（含当前题库问句）。
	used := map[string]bool{}
	if p := normalizeText(req.PromptDE); p != "" {
		used[p] = true
	}
	for _, turn := range req.History {
		if text := normalizeText(turn.Text); text != "" {
			used[text] = true
		}
	}

	fallback := pickTemplate(req.ExamPart, req.Role, req.FollowupCount, used)

	if !h.llm.Enabled() {
		writeJSON(w, http.StatusOK, speakingReplyResp{Reply: fallback, Source: "template"})
		return
	}

	system := examinerSystemPrompt(req.Level, req.Topic)
	if req.Role == "partner" {
		system = partnerSystemPrompt(req.Level, req.Topic)
	}
	var sb strings.Builder
	sb.WriteString("题库当前话题：")
	sb.WriteString(req.Topic)
	sb.WriteString("\n当前考官问句：")
	sb.WriteString(req.PromptDE)
	sb.WriteString("\n对话记录：\n")
	for _, turn := range req.History {
		role := "考生"
		if turn.Role != "user" {
			role = "考官"
		}
		sb.WriteString(role)
		sb.WriteString("：")
		sb.WriteString(strings.TrimSpace(turn.Text))
		sb.WriteString("\n")
	}
	if req.Role == "partner" {
		sb.WriteString("\n请生成搭档对考生最后一句话的回应（德语，一到两句）。")
	} else {
		sb.WriteString("\n请生成考官对考生最后一句话的追问（德语，一到两句）。")
	}

	reply, err := h.llm.Chat(r.Context(), system, sb.String())
	if err != nil {
		h.logger.Printf("speaking llm: %v", err)
		writeJSON(w, http.StatusOK, speakingReplyResp{Reply: fallback, Source: "template"})
		return
	}
	reply = sanitizeReply(reply)
	if !validGermanReply(reply, req.Level) {
		h.logger.Printf("speaking llm output rejected: %q", reply)
		writeJSON(w, http.StatusOK, speakingReplyResp{Reply: fallback, Source: "template"})
		return
	}
	// 与本轮已出现过的问句重复时，改用模板中未被使用的句子。
	if used[normalizeText(reply)] {
		h.logger.Printf("speaking llm output duplicated, fallback to template")
		writeJSON(w, http.StatusOK, speakingReplyResp{Reply: fallback, Source: "template"})
		return
	}
	writeJSON(w, http.StatusOK, speakingReplyResp{Reply: reply, Source: "llm"})
}
