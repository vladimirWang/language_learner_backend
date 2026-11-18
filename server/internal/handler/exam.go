package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// ===== 德专四级（PGG）结构化试题 =====
//
// 试题来自已解析任务的 result.json（Python 阶段 2 产物），
// 同时对应任务目录下的 pgg_exam.db（供客户端离线判分）。

type pggPaper struct {
	PaperID     string   `json:"paper_id"`
	ExamSystem  string   `json:"exam_system"`
	Level       string   `json:"level"`
	Year        int      `json:"year"`
	Title       string   `json:"title"`
	SourceNote  string   `json:"source_note"`
	QuestionCnt int      `json:"question_count"`
	Sections    []string `json:"sections"`
	SourceJobID string   `json:"source_job_id"`
}

type pggQuestion struct {
	PaperID       string   `json:"paper_id"`
	ExamSystem    string   `json:"exam_system"`
	Section       string   `json:"section"`
	QNo           string   `json:"q_no"`
	Stem          string   `json:"stem"`
	Options       []string `json:"options"`
	CorrectAnswer *string  `json:"correct_answer,omitempty"`
	Level         string   `json:"level"`
}

type pggDiktat struct {
	PaperID   string `json:"paper_id"`
	FullText  string `json:"full_text"`
	WordCount int    `json:"word_count"`
	AudioRef  string `json:"audio_ref,omitempty"`
}

type pggWriting struct {
	PaperID     string `json:"paper_id"`
	Prompt      string `json:"prompt"`
	WordLimit   int    `json:"word_limit,omitempty"`
	MaterialRef string `json:"material_ref,omitempty"`
}

// pggMaterialBlock 是原文素材块（阅读篇章 / 听力原文），供讲解定位。
type pggMaterialBlock struct {
	PageNo  int    `json:"page_no"`
	PaperID string `json:"paper_id,omitempty"`
	Section string `json:"section"`
	RawText string `json:"raw_text"`
}

type pggExamFile struct {
	Papers    []pggPaper    `json:"papers"`
	Questions []pggQuestion `json:"questions"`
	Diktat    []pggDiktat   `json:"diktat"`
	Writing   []pggWriting  `json:"writing"`
}

type pggExam struct {
	Papers    []pggPaper
	Questions []pggQuestion
	Diktat    []pggDiktat
	Writing   []pggWriting
	Materials []pggMaterialBlock
}

// loadPGGExam 依次读取成功任务的 result.json，按套卷、题号与原文页去重。
func (h *Handler) loadPGGExam(ctx context.Context) pggExam {
	jobs, err := h.store.ListPDFImportJobs(ctx, 200, false)
	if err != nil {
		h.logger.Printf("list pdf jobs for exam: %v", err)
		return pggExam{}
	}
	exam := pggExam{
		Papers:    make([]pggPaper, 0),
		Questions: make([]pggQuestion, 0),
		Diktat:    make([]pggDiktat, 0),
		Writing:   make([]pggWriting, 0),
		Materials: make([]pggMaterialBlock, 0),
	}
	seenPaper := map[string]bool{}
	seenQuestion := map[string]bool{}
	seenDiktat := map[string]bool{}
	seenWriting := map[string]bool{}
	seenMaterial := map[string]bool{}

	for _, job := range jobs {
		if job.Status != "succeeded" {
			continue
		}
		var summary struct {
			ResultPath string `json:"result_path"`
		}
		if len(job.Result) > 0 {
			if err := json.Unmarshal(job.Result, &summary); err != nil {
				continue
			}
		}
		if summary.ResultPath == "" {
			continue
		}
		raw, err := os.ReadFile(summary.ResultPath)
		if err != nil {
			continue
		}
		var payload struct {
			Exam   *pggExamFile       `json:"exam"`
			Blocks []pggMaterialBlock `json:"blocks"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			continue
		}

		if payload.Exam != nil {
			for _, paper := range payload.Exam.Papers {
				if seenPaper[paper.PaperID] {
					continue
				}
				seenPaper[paper.PaperID] = true
				paper.ExamSystem = "pgg"
				paper.SourceJobID = job.JobID
				exam.Papers = append(exam.Papers, paper)
			}
			for _, question := range payload.Exam.Questions {
				key := question.PaperID + "|" + question.Section + "|" + question.QNo
				if seenQuestion[key] {
					continue
				}
				seenQuestion[key] = true
				exam.Questions = append(exam.Questions, question)
			}
			for _, item := range payload.Exam.Diktat {
				if item.PaperID == "" || seenDiktat[item.PaperID] {
					continue
				}
				seenDiktat[item.PaperID] = true
				exam.Diktat = append(exam.Diktat, item)
			}
			for _, item := range payload.Exam.Writing {
				key := item.PaperID + "|" + item.Prompt
				if item.PaperID == "" || seenWriting[key] {
					continue
				}
				seenWriting[key] = true
				exam.Writing = append(exam.Writing, item)
			}
		}

		for _, block := range payload.Blocks {
			if block.PaperID == "" || strings.TrimSpace(block.RawText) == "" {
				continue
			}
			key := block.PaperID + "|" + block.Section + "|" + strconv.Itoa(block.PageNo)
			if seenMaterial[key] {
				continue
			}
			seenMaterial[key] = true
			exam.Materials = append(exam.Materials, block)
		}
	}

	// 统计每套卷的题量与题型；听写 / 写作单独成题型。
	for index := range exam.Papers {
		sections := make([]string, 0)
		count := 0
		for _, question := range exam.Questions {
			if question.PaperID != exam.Papers[index].PaperID {
				continue
			}
			count++
			if !containsString(sections, question.Section) {
				sections = append(sections, question.Section)
			}
		}
		for _, item := range exam.Diktat {
			if item.PaperID == exam.Papers[index].PaperID && !containsString(sections, "diktat") {
				sections = append(sections, "diktat")
			}
		}
		for _, item := range exam.Writing {
			if item.PaperID == exam.Papers[index].PaperID && !containsString(sections, "writing") {
				sections = append(sections, "writing")
			}
		}
		exam.Papers[index].QuestionCnt = count
		exam.Papers[index].Sections = sections
	}
	sort.Slice(exam.Papers, func(i, j int) bool {
		if exam.Papers[i].Year == exam.Papers[j].Year {
			return exam.Papers[i].PaperID < exam.Papers[j].PaperID
		}
		return exam.Papers[i].Year < exam.Papers[j].Year
	})
	return exam
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func (h *Handler) listExamPapers(c *gin.Context) {
	exam := h.loadPGGExam(c.Request.Context())
	system := strings.TrimSpace(c.Query("system"))
	if system != "" && system != "pgg" {
		writeJSON(c, http.StatusOK, map[string]any{"papers": []pggPaper{}})
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"papers": exam.Papers})
}

func (h *Handler) listExamQuestions(c *gin.Context) {
	paperID := c.Param("paperId")
	section := c.Query("section")
	exam := h.loadPGGExam(c.Request.Context())
	out := make([]pggQuestion, 0)
	for _, question := range exam.Questions {
		if question.PaperID != paperID {
			continue
		}
		if section != "" && question.Section != section {
			continue
		}
		// 不向前端泄露答案，判分由提交接口完成。
		question.CorrectAnswer = nil
		out = append(out, question)
	}
	writeJSON(c, http.StatusOK, map[string]any{"questions": out})
}

// getExamQuestion 返回单题（不含答案），qid 由 paper_id + section + q_no 定位。
func (h *Handler) getExamQuestion(c *gin.Context) {
	paperID := strings.TrimSpace(c.Query("paper_id"))
	section := strings.TrimSpace(c.Query("section"))
	qNo := strings.TrimSpace(c.Query("q_no"))
	if paperID == "" || section == "" || qNo == "" {
		writeError(c, http.StatusBadRequest, "paper_id, section and q_no are required")
		return
	}
	exam := h.loadPGGExam(c.Request.Context())
	for _, question := range exam.Questions {
		if question.PaperID != paperID || question.Section != section || question.QNo != qNo {
			continue
		}
		question.CorrectAnswer = nil
		writeJSON(c, http.StatusOK, map[string]any{"question": question})
		return
	}
	writeError(c, http.StatusNotFound, "question not found")
}

func (h *Handler) listExamDiktat(c *gin.Context) {
	paperID := c.Param("paperId")
	exam := h.loadPGGExam(c.Request.Context())
	out := make([]pggDiktat, 0)
	for _, item := range exam.Diktat {
		if item.PaperID == paperID {
			out = append(out, item)
		}
	}
	writeJSON(c, http.StatusOK, map[string]any{"diktat": out})
}

func (h *Handler) listExamWriting(c *gin.Context) {
	paperID := c.Param("paperId")
	exam := h.loadPGGExam(c.Request.Context())
	out := make([]pggWriting, 0)
	for _, item := range exam.Writing {
		if item.PaperID == paperID {
			out = append(out, item)
		}
	}
	writeJSON(c, http.StatusOK, map[string]any{"writing": out})
}

// examMaterial 返回某套卷某题型的原文素材，供讲解定位。
func (h *Handler) examMaterial(c *gin.Context) {
	paperID := strings.TrimSpace(c.Query("paper_id"))
	section := strings.TrimSpace(c.Query("section"))
	if paperID == "" || section == "" {
		writeError(c, http.StatusBadRequest, "paper_id and section are required")
		return
	}
	exam := h.loadPGGExam(c.Request.Context())
	out := make([]pggMaterialBlock, 0)
	for _, block := range exam.Materials {
		if block.PaperID == paperID && block.Section == section {
			out = append(out, block)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PageNo < out[j].PageNo })
	writeJSON(c, http.StatusOK, map[string]any{
		"paper_id":  paperID,
		"section":   section,
		"materials": out,
	})
}

func normalizeExamAnswer(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if value[0] >= 'A' && value[0] <= 'D' {
		return string(value[0])
	}
	return value
}

func (h *Handler) submitExamAnswer(c *gin.Context) {
	var req struct {
		PaperID string `json:"paper_id"`
		Section string `json:"section"`
		QNo     string `json:"q_no"`
		Answer  string `json:"answer"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.PaperID == "" || req.Section == "" || req.QNo == "" {
		writeError(c, http.StatusBadRequest, "paper_id, section and q_no are required")
		return
	}
	exam := h.loadPGGExam(c.Request.Context())
	for _, question := range exam.Questions {
		if question.PaperID != req.PaperID || question.Section != req.Section || question.QNo != req.QNo {
			continue
		}
		if question.CorrectAnswer == nil || *question.CorrectAnswer == "" {
			writeJSON(c, http.StatusOK, map[string]any{
				"is_correct":     nil,
				"correct_answer": nil,
				"message":        "该题尚未绑定标准答案",
			})
			return
		}
		isCorrect := normalizeExamAnswer(req.Answer) == normalizeExamAnswer(*question.CorrectAnswer)
		writeJSON(c, http.StatusOK, map[string]any{
			"is_correct":     isCorrect,
			"correct_answer": normalizeExamAnswer(*question.CorrectAnswer),
		})
		return
	}
	writeError(c, http.StatusNotFound, "question not found")
}
