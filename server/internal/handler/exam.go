package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
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

type pggExamFile struct {
	Papers    []pggPaper    `json:"papers"`
	Questions []pggQuestion `json:"questions"`
}

// loadPGGExam 依次读取成功任务的 result.json，按套卷与题号去重。
func (h *Handler) loadPGGExam(ctx context.Context) ([]pggPaper, []pggQuestion) {
	jobs, err := h.store.ListPDFImportJobs(ctx, 200, false)
	if err != nil {
		h.logger.Printf("list pdf jobs for exam: %v", err)
		return nil, nil
	}
	papers := make([]pggPaper, 0)
	questions := make([]pggQuestion, 0)
	seenPaper := map[string]bool{}
	seenQuestion := map[string]bool{}

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
			Exam *pggExamFile `json:"exam"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil || payload.Exam == nil {
			continue
		}
		for _, paper := range payload.Exam.Papers {
			if seenPaper[paper.PaperID] {
				continue
			}
			seenPaper[paper.PaperID] = true
			paper.ExamSystem = "pgg"
			paper.SourceJobID = job.JobID
			papers = append(papers, paper)
		}
		for _, question := range payload.Exam.Questions {
			key := question.PaperID + "|" + question.Section + "|" + question.QNo
			if seenQuestion[key] {
				continue
			}
			seenQuestion[key] = true
			questions = append(questions, question)
		}
	}

	// 统计每套卷的题量与题型。
	for index := range papers {
		sections := make([]string, 0)
		count := 0
		for _, question := range questions {
			if question.PaperID != papers[index].PaperID {
				continue
			}
			count++
			found := false
			for _, section := range sections {
				if section == question.Section {
					found = true
					break
				}
			}
			if !found {
				sections = append(sections, question.Section)
			}
		}
		papers[index].QuestionCnt = count
		papers[index].Sections = sections
	}
	sort.Slice(papers, func(i, j int) bool {
		if papers[i].Year == papers[j].Year {
			return papers[i].PaperID < papers[j].PaperID
		}
		return papers[i].Year < papers[j].Year
	})
	return papers, questions
}

func (h *Handler) listExamPapers(c *gin.Context) {
	papers, _ := h.loadPGGExam(c.Request.Context())
	writeJSON(c, http.StatusOK, map[string]any{"papers": papers})
}

func (h *Handler) listExamQuestions(c *gin.Context) {
	paperID := c.Param("paperId")
	section := c.Query("section")
	_, questions := h.loadPGGExam(c.Request.Context())
	out := make([]pggQuestion, 0)
	for _, question := range questions {
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
	_, questions := h.loadPGGExam(c.Request.Context())
	for _, question := range questions {
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
