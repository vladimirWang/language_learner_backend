package handler

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// adminListGrammarQuestions 列出待审/已审语法题，供管理台查看。
func (h *Handler) adminListGrammarQuestions(c *gin.Context) {
	lang := strings.TrimSpace(c.Query("lang"))
	if lang != "" && !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	if level != "" && !challengeLevelPattern.MatchString(level) {
		writeError(c, http.StatusBadRequest, "invalid level")
		return
	}
	status := normalizeGrammarStatus(c.Query("status"))
	if strings.TrimSpace(c.Query("status")) != "" && status == "" {
		writeError(c, http.StatusBadRequest, "invalid status")
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	list, err := h.store.ListGrammarQuestions(c.Request.Context(), lang, level, status, limit)
	if err != nil {
		h.logger.Printf("admin list grammar questions: %v", err)
		writeError(c, http.StatusInternalServerError, "list grammar questions failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"questions": list})
}

// adminImportGrammarDrafts 接收 agent 生成的语法题草稿，统一入库为 draft。
func (h *Handler) adminImportGrammarDrafts(c *gin.Context) {
	var body struct {
		Questions []struct {
			Lang        string   `json:"lang"`
			Level       string   `json:"level"`
			PointKey    string   `json:"point_key"`
			PointLabel  string   `json:"point_label"`
			Stem        string   `json:"stem"`
			Options     []string `json:"options"`
			Answer      string   `json:"answer"`
			Explanation string   `json:"explanation"`
			SourcePath  string   `json:"source_path"`
			SourceSHA   string   `json:"source_sha"`
			GeneratedBy string   `json:"generated_by"`
		} `json:"questions"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	if len(body.Questions) == 0 {
		writeError(c, http.StatusBadRequest, "questions is empty")
		return
	}
	if len(body.Questions) > 500 {
		writeError(c, http.StatusBadRequest, "too many questions in one batch")
		return
	}

	drafts := make([]store.GrammarQuestion, 0, len(body.Questions))
	skipped := make([]string, 0)
	for i, q := range body.Questions {
		draft, err := buildGrammarDraft(q.Lang, q.Level, q.PointKey, q.PointLabel, q.Stem,
			q.Options, q.Answer, q.Explanation, q.SourcePath, q.SourceSHA, q.GeneratedBy)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("第 %d 题：%v", i+1, err))
			continue
		}
		drafts = append(drafts, draft)
	}
	if len(drafts) == 0 {
		writeJSON(c, http.StatusOK, map[string]any{"written": 0, "skipped": skipped})
		return
	}
	written, err := h.store.UpsertGrammarDrafts(c.Request.Context(), drafts)
	if err != nil {
		h.logger.Printf("import grammar drafts: %v", err)
		writeError(c, http.StatusInternalServerError, "import grammar drafts failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"written": written, "skipped": skipped})
}

// adminExportGrammarCSV 导出待审语法题，可直接在表格中把 status 改为 approved/rejected 后回传。
func (h *Handler) adminExportGrammarCSV(c *gin.Context) {
	lang := strings.TrimSpace(c.Query("lang"))
	if lang != "" && !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	if level != "" && !challengeLevelPattern.MatchString(level) {
		writeError(c, http.StatusBadRequest, "invalid level")
		return
	}
	status := normalizeGrammarStatus(c.Query("status"))
	if strings.TrimSpace(c.Query("status")) != "" && status == "" {
		writeError(c, http.StatusBadRequest, "invalid status")
		return
	}
	if status == "" {
		status = store.GrammarStatusDraft
	}
	list, err := h.store.ListGrammarQuestions(c.Request.Context(), lang, level, status, 1000)
	if err != nil {
		h.logger.Printf("export grammar questions: %v", err)
		writeError(c, http.StatusInternalServerError, "export grammar questions failed")
		return
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="grammar-questions.csv"`)
	writer := csv.NewWriter(c.Writer)
	_ = writer.Write([]string{
		"id", "lang", "level", "point_key", "point_label", "stem", "options",
		"answer", "explanation", "source_path", "status", "review_note",
	})
	for _, q := range list {
		var options []string
		_ = json.Unmarshal(q.Options, &options)
		_ = writer.Write([]string{
			strconv.FormatInt(q.ID, 10), q.Lang, q.Level, q.PointKey, q.PointLabel, q.Stem,
			strings.Join(options, " | "), q.Answer, q.Explanation, q.SourcePath, q.Status, q.ReviewNote,
		})
	}
	writer.Flush()
}

// adminImportGrammarCSV 回传审核后的 CSV，只更新 status 与 review_note。
func (h *Handler) adminImportGrammarCSV(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		writeError(c, http.StatusBadRequest, "缺少 CSV 文件字段 file")
		return
	}
	opened, err := file.Open()
	if err != nil {
		writeError(c, http.StatusBadRequest, "读取 CSV 失败")
		return
	}
	defer opened.Close()

	reader := csv.NewReader(opened)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		writeError(c, http.StatusBadRequest, "解析 CSV 失败")
		return
	}
	if len(rows) < 2 {
		writeError(c, http.StatusBadRequest, "CSV 没有数据行")
		return
	}
	header := make(map[string]int, len(rows[0]))
	for i, key := range rows[0] {
		header[strings.ToLower(strings.TrimSpace(key))] = i
	}
	idIndex, ok := header["id"]
	if !ok {
		writeError(c, http.StatusBadRequest, "CSV 缺少 id 列")
		return
	}
	statusIndex, ok := header["status"]
	if !ok {
		if statusIndex, ok = header["result"]; !ok {
			writeError(c, http.StatusBadRequest, "CSV 缺少 status 或 result 列")
			return
		}
	}
	noteIndex, hasNote := header["review_note"]

	grouped := make(map[string][]int64)
	skipped := make([]string, 0)
	for i, row := range rows[1:] {
		if len(row) <= idIndex {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSpace(row[idIndex]), 10, 64)
		if err != nil || id <= 0 {
			skipped = append(skipped, fmt.Sprintf("第 %d 行：id 无效", i+2))
			continue
		}
		if statusIndex >= len(row) {
			skipped = append(skipped, fmt.Sprintf("第 %d 行：缺少 status", i+2))
			continue
		}
		status := normalizeGrammarStatus(row[statusIndex])
		if status == "" {
			skipped = append(skipped, fmt.Sprintf("第 %d 行：status 无法识别（%s）", i+2, row[statusIndex]))
			continue
		}
		note := ""
		if hasNote && noteIndex < len(row) {
			note = strings.TrimSpace(row[noteIndex])
		}
		key := status + "\x00" + note
		grouped[key] = append(grouped[key], id)
	}

	var updated int64
	for key, ids := range grouped {
		parts := strings.SplitN(key, "\x00", 2)
		n, err := h.store.ReviewGrammarQuestions(c.Request.Context(), ids, parts[0], parts[1])
		if err != nil {
			h.logger.Printf("review grammar questions: %v", err)
			writeError(c, http.StatusInternalServerError, "更新审核状态失败")
			return
		}
		updated += n
	}
	writeJSON(c, http.StatusOK, map[string]any{"updated": updated, "skipped": skipped})
}

// buildGrammarDraft 校验 agent 草稿并补齐默认值。
func buildGrammarDraft(lang, level, pointKey, pointLabel, stem string, options []string,
	answer, explanation, sourcePath, sourceSHA, generatedBy string) (store.GrammarQuestion, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if !langPattern.MatchString(lang) {
		return store.GrammarQuestion{}, errors.New("lang 无效")
	}
	level = strings.ToUpper(strings.TrimSpace(level))
	if level != "" && !challengeLevelPattern.MatchString(level) {
		return store.GrammarQuestion{}, errors.New("level 无效")
	}
	stem = strings.TrimSpace(stem)
	if stem == "" {
		return store.GrammarQuestion{}, errors.New("stem 为空")
	}
	if len([]rune(stem)) > 500 {
		return store.GrammarQuestion{}, errors.New("stem 超过 500 字")
	}
	cleaned := make([]string, 0, len(options))
	seen := make(map[string]bool, len(options))
	for _, opt := range options {
		opt = strings.TrimSpace(opt)
		if opt == "" || seen[strings.ToLower(opt)] {
			continue
		}
		seen[strings.ToLower(opt)] = true
		cleaned = append(cleaned, opt)
	}
	if len(cleaned) < 2 || len(cleaned) > 6 {
		return store.GrammarQuestion{}, errors.New("options 需为 2~6 个不重复选项")
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return store.GrammarQuestion{}, errors.New("answer 为空")
	}
	matched := ""
	for _, opt := range cleaned {
		if strings.EqualFold(opt, answer) {
			matched = opt
			break
		}
	}
	if matched == "" {
		return store.GrammarQuestion{}, errors.New("answer 不在 options 中")
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return store.GrammarQuestion{}, err
	}
	pointKey = strings.TrimSpace(pointKey)
	pointLabel = strings.TrimSpace(pointLabel)
	if pointLabel == "" {
		pointLabel = pointKey
	}
	generatedBy = strings.TrimSpace(generatedBy)
	if generatedBy == "" {
		generatedBy = "agent"
	}
	return store.GrammarQuestion{
		Lang:        lang,
		Level:       level,
		PointKey:    pointKey,
		PointLabel:  pointLabel,
		Stem:        stem,
		Options:     encoded,
		Answer:      matched,
		Explanation: strings.TrimSpace(explanation),
		SourcePath:  strings.TrimSpace(sourcePath),
		SourceSHA:   strings.TrimSpace(sourceSHA),
		GeneratedBy: generatedBy,
		Status:      store.GrammarStatusDraft,
	}, nil
}

// normalizeGrammarStatus 兼容中文与常见英文写法；无法识别返回空串。
func normalizeGrammarStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "approved", "approve", "pass", "passed", "yes", "y", "通过", "已通过", "可用":
		return store.GrammarStatusApproved
	case "rejected", "reject", "no", "n", "驳回", "不通过", "废弃":
		return store.GrammarStatusRejected
	case "draft", "pending", "待审", "待审核":
		return store.GrammarStatusDraft
	}
	return ""
}
