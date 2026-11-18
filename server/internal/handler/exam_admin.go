package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// 管理台 PGG 题库维护：浏览试卷/题目、绑定标准答案、查看听写与写作。
// 数据直接写回对应解析任务的 result.json，客户端判分接口即时生效。

var examFileMu sync.Mutex

type examAdminPaper struct {
	pggPaper
	AnsweredCount int `json:"answered_count"`
	DiktatCount   int `json:"diktat_count"`
	WritingCount  int `json:"writing_count"`
}

func (h *Handler) examAdminPapers(c *gin.Context) {
	exam := h.loadPGGExam(c.Request.Context())
	out := make([]examAdminPaper, 0, len(exam.Papers))
	for _, paper := range exam.Papers {
		item := examAdminPaper{pggPaper: paper}
		for _, question := range exam.Questions {
			if question.PaperID != paper.PaperID {
				continue
			}
			if question.CorrectAnswer != nil && *question.CorrectAnswer != "" {
				item.AnsweredCount++
			}
		}
		for _, diktat := range exam.Diktat {
			if diktat.PaperID == paper.PaperID {
				item.DiktatCount++
			}
		}
		for _, writing := range exam.Writing {
			if writing.PaperID == paper.PaperID {
				item.WritingCount++
			}
		}
		out = append(out, item)
	}
	writeJSON(c, http.StatusOK, map[string]any{"papers": out})
}

func (h *Handler) examAdminQuestions(c *gin.Context) {
	paperID := strings.TrimSpace(c.Query("paper_id"))
	if paperID == "" {
		writeError(c, http.StatusBadRequest, "paper_id is required")
		return
	}
	section := strings.TrimSpace(c.Query("section"))
	exam := h.loadPGGExam(c.Request.Context())
	out := make([]pggQuestion, 0)
	for _, question := range exam.Questions {
		if question.PaperID != paperID {
			continue
		}
		if section != "" && question.Section != section {
			continue
		}
		out = append(out, question)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Section != out[j].Section {
			return out[i].Section < out[j].Section
		}
		return out[i].QNo < out[j].QNo
	})
	writeJSON(c, http.StatusOK, map[string]any{"questions": out})
}

// examAdminUpdateAnswer 绑定或清除单题标准答案（空字符串表示解除绑定）。
func (h *Handler) examAdminUpdateAnswer(c *gin.Context) {
	var req struct {
		PaperID       string `json:"paper_id"`
		Section       string `json:"section"`
		QNo           string `json:"q_no"`
		CorrectAnswer string `json:"correct_answer"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	req.PaperID = strings.TrimSpace(req.PaperID)
	req.Section = strings.TrimSpace(req.Section)
	req.QNo = strings.TrimSpace(req.QNo)
	answer := normalizeExamAnswer(req.CorrectAnswer)
	if req.PaperID == "" || req.Section == "" || req.QNo == "" {
		writeError(c, http.StatusBadRequest, "paper_id, section and q_no are required")
		return
	}
	if answer != "" && answer != "A" && answer != "B" && answer != "C" && answer != "D" {
		writeError(c, http.StatusBadRequest, "correct_answer must be A/B/C/D or empty")
		return
	}

	examFileMu.Lock()
	defer examFileMu.Unlock()

	path, err := h.findExamResultFile(c, req.PaperID)
	if err != nil {
		writeError(c, http.StatusNotFound, err.Error())
		return
	}
	updated, err := rewriteExamAnswer(path, req.PaperID, req.Section, req.QNo, answer)
	if err != nil {
		h.logger.Printf("update exam answer %s: %v", path, err)
		writeError(c, http.StatusInternalServerError, "写入答案失败")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"question": updated})
}

// findExamResultFile 定位包含指定试卷的 result.json。
func (h *Handler) findExamResultFile(c *gin.Context, paperID string) (string, error) {
	jobs, err := h.store.ListPDFImportJobs(c.Request.Context(), 200, false)
	if err != nil {
		return "", err
	}
	for _, job := range jobs {
		if job.Status != "succeeded" || len(job.Result) == 0 {
			continue
		}
		var summary struct {
			ResultPath string `json:"result_path"`
		}
		if err := json.Unmarshal(job.Result, &summary); err != nil || summary.ResultPath == "" {
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
			if paper.PaperID == paperID {
				return summary.ResultPath, nil
			}
		}
	}
	return "", os.ErrNotExist
}

// rewriteExamAnswer 以 map 方式改字段，避免丢 result.json 中的其他字段。
func rewriteExamAnswer(path, paperID, section, qNo, answer string) (pggQuestion, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return pggQuestion{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return pggQuestion{}, err
	}
	exam, _ := payload["exam"].(map[string]any)
	items, _ := exam["questions"].([]any)
	for _, item := range items {
		question, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if question["paper_id"] != paperID || question["section"] != section || question["q_no"] != qNo {
			continue
		}
		if answer == "" {
			question["correct_answer"] = nil
		} else {
			question["correct_answer"] = answer
		}
		if err := writeJSONFileAtomic(path, payload); err != nil {
			return pggQuestion{}, err
		}
		encoded, err := json.Marshal(question)
		if err != nil {
			return pggQuestion{}, err
		}
		var updated pggQuestion
		if err := json.Unmarshal(encoded, &updated); err != nil {
			return pggQuestion{}, err
		}
		return updated, nil
	}
	return pggQuestion{}, os.ErrNotExist
}

func writeJSONFileAtomic(path string, payload any) error {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".result-*.json")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
