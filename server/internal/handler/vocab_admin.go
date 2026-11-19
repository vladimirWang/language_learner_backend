package handler

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// validVocabWordTypes 与 builder 各语种插件的 word_types 保持一致，保证管理台录入的词可回写 CSV。
var validVocabWordTypes = map[string]bool{
	"noun": true, "verb": true, "adj": true,
	"adv": true, "prep": true, "particle": true,
}

// validVocabLevels 覆盖 CEFR（de/en）与 JLPT（ja）。
var validVocabLevels = map[string]bool{
	"A1": true, "A2": true, "B1": true, "B2": true, "C1": true, "C2": true,
	"N1": true, "N2": true, "N3": true, "N4": true, "N5": true,
}

// vocabWordUpsert 是新增/更新词条的请求体。
type vocabWordUpsert struct {
	Lang         string `json:"lang"`
	Lemma        string `json:"lemma"`
	Level        string `json:"level"`
	WordType     string `json:"word_type"`
	Gender       string `json:"gender"`
	Plural       string `json:"plural"`
	ChineseMean  string `json:"chinese_mean"`
	EnglishMean  string `json:"english_mean"`
	ExampleDE    string `json:"example_de"`
	SourceMDPath string `json:"source_md_path"`
}

// normalizeVocabWord 校验并规范化词条字段；返回中文错误描述。
func normalizeVocabWord(req vocabWordUpsert, forcedLang string) (store.VocabWordInput, string) {
	req.Lang = strings.TrimSpace(req.Lang)
	if req.Lang == "" {
		req.Lang = forcedLang
	}
	if req.Lang == "" {
		req.Lang = "de"
	}
	if !langPattern.MatchString(req.Lang) {
		return store.VocabWordInput{}, "invalid lang"
	}
	if forcedLang != "" && req.Lang != forcedLang {
		return store.VocabWordInput{}, "lang 与所选语种不一致"
	}

	req.Lemma = strings.TrimSpace(req.Lemma)
	if req.Lemma == "" {
		return store.VocabWordInput{}, "词条（lemma）不能为空"
	}
	if utf8.RuneCountInString(req.Lemma) > 80 {
		return store.VocabWordInput{}, "词条（lemma）不能超过 80 个字符"
	}

	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	if !validVocabLevels[req.Level] {
		return store.VocabWordInput{}, "无效等级（A1–C2 / N5–N1）"
	}

	req.WordType = strings.ToLower(strings.TrimSpace(req.WordType))
	if !validVocabWordTypes[req.WordType] {
		return store.VocabWordInput{}, "无效词性（noun/verb/adj/adv/prep/particle）"
	}

	req.Gender = strings.ToLower(strings.TrimSpace(req.Gender))
	if req.Gender != "" && req.Gender != "m" && req.Gender != "f" && req.Gender != "n" {
		return store.VocabWordInput{}, "无效词性性别（m/f/n）"
	}
	if req.WordType == "noun" && req.Lang == "de" && req.Gender == "" {
		return store.VocabWordInput{}, "德语名词必须填写词性性别（m/f/n）"
	}
	if req.WordType != "noun" && req.Gender != "" {
		return store.VocabWordInput{}, "只有名词可以填写词性性别"
	}

	req.Plural = strings.TrimSpace(req.Plural)
	req.ChineseMean = strings.TrimSpace(req.ChineseMean)
	if req.ChineseMean == "" {
		return store.VocabWordInput{}, "中文释义不能为空"
	}
	if utf8.RuneCountInString(req.ChineseMean) > 200 {
		return store.VocabWordInput{}, "中文释义不能超过 200 个字符"
	}

	req.EnglishMean = strings.TrimSpace(req.EnglishMean)
	req.ExampleDE = strings.TrimSpace(req.ExampleDE)
	req.SourceMDPath = strings.TrimSpace(req.SourceMDPath)
	if utf8.RuneCountInString(req.EnglishMean) > 200 || utf8.RuneCountInString(req.ExampleDE) > 500 ||
		utf8.RuneCountInString(req.SourceMDPath) > 300 || utf8.RuneCountInString(req.Plural) > 80 {
		return store.VocabWordInput{}, "字段长度超出限制"
	}

	return store.VocabWordInput{
		Lang:         req.Lang,
		Lemma:        req.Lemma,
		Level:        req.Level,
		WordType:     req.WordType,
		Gender:       req.Gender,
		Plural:       req.Plural,
		ChineseMean:  req.ChineseMean,
		EnglishMean:  req.EnglishMean,
		ExampleDE:    req.ExampleDE,
		SourceMDPath: req.SourceMDPath,
	}, ""
}

// listVocabWords 返回新词库列表。支持 ?lang=&level=&q=&limit=&offset=。
func (h *Handler) listVocabWords(c *gin.Context) {
	lang := strings.TrimSpace(c.Query("lang"))
	if lang != "" && !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	if level != "" && !validVocabLevels[level] {
		writeError(c, http.StatusBadRequest, "invalid level")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		offset = 0
	}

	words, total, err := h.store.ListVocabWords(c.Request.Context(), lang, level, strings.TrimSpace(c.Query("q")), limit, offset)
	if err != nil {
		h.logger.Printf("list vocab words lang=%s: %v", lang, err)
		writeError(c, http.StatusInternalServerError, "list vocab words failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"words":  words,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// createVocabWord 新增词条；(lang, lemma) 已存在时返回 409。
func (h *Handler) createVocabWord(c *gin.Context) {
	var req vocabWordUpsert
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	input, msg := normalizeVocabWord(req, "")
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	id, created, err := h.store.CreateVocabWord(c.Request.Context(), input)
	if err != nil {
		h.logger.Printf("create vocab word %s/%s: %v", input.Lang, input.Lemma, err)
		writeError(c, http.StatusInternalServerError, "create vocab word failed")
		return
	}
	if !created {
		writeError(c, http.StatusConflict, "该词条已存在，请直接编辑")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"word_id": id})
}

// updateVocabWord 按 ID 更新词条。
func (h *Handler) updateVocabWord(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid word id")
		return
	}
	var req vocabWordUpsert
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	input, msg := normalizeVocabWord(req, "")
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	ok, err := h.store.UpdateVocabWord(c.Request.Context(), id, input)
	if err != nil {
		h.logger.Printf("update vocab word %d: %v", id, err)
		writeError(c, http.StatusConflict, "保存失败：同语种下该词条已存在")
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "词条不存在")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// deleteVocabWord 按 ID 删除词条。
func (h *Handler) deleteVocabWord(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid word id")
		return
	}
	ok, err := h.store.DeleteVocabWord(c.Request.Context(), id)
	if err != nil {
		h.logger.Printf("delete vocab word %d: %v", id, err)
		writeError(c, http.StatusInternalServerError, "delete vocab word failed")
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "词条不存在")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// importVocabWords 导入 CSV 文本；任一行校验失败时整批不写入。
func (h *Handler) importVocabWords(c *gin.Context) {
	var req struct {
		Lang   string `json:"lang"`
		CSV    string `json:"csv"`
		DryRun bool   `json:"dry_run"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 8<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	req.Lang = strings.TrimSpace(req.Lang)
	if req.Lang == "" {
		req.Lang = "de"
	}
	if !langPattern.MatchString(req.Lang) {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}

	rows, errs := parseVocabCSV(req.Lang, req.CSV)
	if len(errs) > 0 {
		c.JSON(http.StatusBadRequest, map[string]any{
			"error":  "CSV 校验未通过，未写入任何词条",
			"errors": errs,
		})
		return
	}
	if len(rows) == 0 {
		writeError(c, http.StatusBadRequest, "CSV 中没有可导入的词条")
		return
	}
	if req.DryRun {
		writeJSON(c, http.StatusOK, map[string]any{
			"dry_run": true, "inserted": 0, "updated": 0, "total": len(rows),
		})
		return
	}

	inserted, updated, err := h.store.UpsertVocabWords(c.Request.Context(), rows)
	if err != nil {
		h.logger.Printf("import vocab words lang=%s: %v", req.Lang, err)
		writeError(c, http.StatusInternalServerError, "import vocab words failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"dry_run": false, "inserted": inserted, "updated": updated, "total": len(rows),
	})
}

// parseVocabCSV 解析管理台导入的 CSV。支持与 content/<lang>/vocab.csv 相同的列，
// 额外忽略 builder 专用列；行内 lang 可省略，省略时使用请求语种。
func parseVocabCSV(lang, raw string) ([]store.VocabWordInput, []string) {
	raw = strings.TrimPrefix(raw, "\ufeff")
	reader := csv.NewReader(strings.NewReader(raw))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		if err == io.EOF {
			return nil, []string{"CSV 内容为空"}
		}
		return nil, []string{"CSV 解析失败：" + err.Error()}
	}
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[strings.ToLower(strings.TrimSpace(name))] = i
	}
	var missing []string
	for _, required := range []string{"lemma", "level", "chinese_mean"} {
		if _, ok := col[required]; !ok {
			missing = append(missing, required)
		}
	}
	if len(missing) > 0 {
		return nil, []string{"CSV 缺少必需列：" + strings.Join(missing, ", ")}
	}

	pick := func(record []string, name string) string {
		idx, ok := col[name]
		if !ok || idx >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[idx])
	}

	rows := make([]store.VocabWordInput, 0, 64)
	errors := make([]string, 0)
	seen := make(map[string]bool)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			errors = append(errors, "CSV 解析失败："+err.Error())
			break
		}
		if len(strings.TrimSpace(strings.Join(record, ""))) == 0 {
			continue
		}
		line, _ := reader.FieldPos(0)
		lemma := pick(record, "lemma")
		if seen[lemma] {
			errors = append(errors, "第 "+strconv.Itoa(line)+" 行：重复词条 "+lemma)
			continue
		}
		seen[lemma] = true
		input, msg := normalizeVocabWord(vocabWordUpsert{
			Lang:         pick(record, "lang"),
			Lemma:        lemma,
			Level:        pick(record, "level"),
			WordType:     pick(record, "word_type"),
			Gender:       pick(record, "gender"),
			Plural:       pick(record, "plural"),
			ChineseMean:  pick(record, "chinese_mean"),
			EnglishMean:  pick(record, "english_mean"),
			ExampleDE:    pick(record, "example_de"),
			SourceMDPath: pick(record, "source_md_path"),
		}, lang)
		if msg != "" {
			errors = append(errors, "第 "+strconv.Itoa(line)+" 行："+msg)
			continue
		}
		rows = append(rows, input)
		if len(errors) >= 50 {
			errors = append(errors, "错误过多，仅展示前 50 条")
			break
		}
	}
	return rows, errors
}
