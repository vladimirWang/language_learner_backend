package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// pdfJobs 返回最近的 PDF 导入任务。
func (h *Handler) pdfJobs(c *gin.Context) {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			limit = n
		} else {
			writeError(c, http.StatusBadRequest, "limit must be 1..500")
			return
		}
	}
	includeDeleted := c.Query("include_deleted") == "1"
	jobs, err := h.store.ListPDFImportJobs(c.Request.Context(), limit, includeDeleted)
	if err != nil {
		h.logger.Printf("list pdf jobs: %v", err)
		writeError(c, http.StatusInternalServerError, "query jobs failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"jobs": jobs})
}

// pdfJobDetail 返回任务与日志；after_log_id 用于增量刷新。
func (h *Handler) pdfJobDetail(c *gin.Context) {
	jobID := c.Param("jobId")
	afterLogID, _ := strconv.ParseInt(c.Query("after_log_id"), 10, 64)
	job, err := h.store.GetPDFImportJob(c.Request.Context(), jobID)
	if err != nil {
		h.logger.Printf("get pdf job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "query job failed")
		return
	}
	if job == nil {
		writeError(c, http.StatusNotFound, "job not found")
		return
	}
	logs, err := h.store.ListPDFImportLogs(c.Request.Context(), jobID, afterLogID)
	if err != nil {
		h.logger.Printf("list pdf logs %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "query logs failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"job": job, "logs": logs})
}

// pdfBlock 是解析结果中的一页文本块，供人工校对。
type pdfBlock struct {
	PageNo     int    `json:"page_no"`
	PaperID    string `json:"paper_id"`
	ExamSystem string `json:"exam_system"`
	Lang       string `json:"lang"`
	Section    string `json:"section"`
	RawText    string `json:"raw_text"`
}

type pdfPaper struct {
	PaperID  string   `json:"paper_id"`
	Pages    []int    `json:"pages"`
	Sections []string `json:"sections"`
}

type pdfParseResult struct {
	Lang              string         `json:"lang"`
	Source            string         `json:"source"`
	PageCount         int            `json:"page_count"`
	OCRPPages         []int          `json:"ocr_pages,omitempty"`
	Papers            []pdfPaper     `json:"papers"`
	SectionPageCounts map[string]int `json:"section_page_counts"`
	Blocks            []pdfBlock     `json:"blocks"`
	Exam              *pggExamFile   `json:"exam,omitempty"`
	ExamDBPath        string         `json:"exam_db_path,omitempty"`
}

var validPDFSections = map[string]bool{
	"diktat": true, "hoeren": true, "lesen": true, "grammar": true,
	"writing": true, "unknown": true,
}

// pdfResultPath 返回任务的解析结果文件路径（与上传文件同目录）。
func pdfResultPath(job *store.PDFImportJob) string {
	return filepath.Join(filepath.Dir(job.StoredPath), "result.json")
}

// loadPDFResult 读取解析结果文件。
func loadPDFResult(job *store.PDFImportJob) (*pdfParseResult, error) {
	raw, err := os.ReadFile(pdfResultPath(job))
	if err != nil {
		return nil, err
	}
	var result pdfParseResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// pdfJobBlocks 返回可校对的解析块。
func (h *Handler) pdfJobBlocks(c *gin.Context) {
	job, err := h.store.GetPDFImportJob(c.Request.Context(), c.Param("jobId"))
	if err != nil {
		h.logger.Printf("get pdf job: %v", err)
		writeError(c, http.StatusInternalServerError, "query job failed")
		return
	}
	if job == nil {
		writeError(c, http.StatusNotFound, "job not found")
		return
	}
	result, err := loadPDFResult(job)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(c, http.StatusConflict, "解析结果尚未生成")
			return
		}
		h.logger.Printf("load pdf result %s: %v", job.JobID, err)
		writeError(c, http.StatusInternalServerError, "解析结果读取失败")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"job_id": job.JobID, "result": result})
}

// pdfUpdateBlocks 保存人工校对后的解析块，并回写数据库摘要。
func (h *Handler) pdfUpdateBlocks(c *gin.Context) {
	jobID := c.Param("jobId")
	job, err := h.store.GetPDFImportJob(c.Request.Context(), jobID)
	if err != nil {
		h.logger.Printf("get pdf job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "query job failed")
		return
	}
	if job == nil {
		writeError(c, http.StatusNotFound, "job not found")
		return
	}

	var req struct {
		Blocks []pdfBlock `json:"blocks"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 32<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if len(req.Blocks) == 0 {
		writeError(c, http.StatusBadRequest, "blocks is required")
		return
	}
	if len(req.Blocks) > 20000 {
		writeError(c, http.StatusBadRequest, "too many blocks (max 20000)")
		return
	}

	lang := job.Lang
	previous, err := loadPDFResult(job)
	if err == nil {
		lang = previous.Lang
	}
	paperPages := make(map[string][]int)
	paperSections := make(map[string][]string)
	sectionCounts := make(map[string]int)
	for index := range req.Blocks {
		block := &req.Blocks[index]
		if block.PageNo <= 0 {
			writeError(c, http.StatusBadRequest, "page_no must be positive")
			return
		}
		if !validPDFSections[block.Section] {
			writeError(c, http.StatusBadRequest, "invalid section: "+block.Section)
			return
		}
		block.PaperID = strings.TrimSpace(block.PaperID)
		if block.ExamSystem == "" {
			block.ExamSystem = "pgg"
		}
		if block.Lang == "" {
			block.Lang = lang
		}
		sectionCounts[block.Section]++
		if block.PaperID == "" {
			continue
		}
		paperPages[block.PaperID] = append(paperPages[block.PaperID], block.PageNo)
		found := false
		for _, section := range paperSections[block.PaperID] {
			if section == block.Section {
				found = true
				break
			}
		}
		if !found {
			paperSections[block.PaperID] = append(paperSections[block.PaperID], block.Section)
		}
	}

	papers := make([]pdfPaper, 0, len(paperPages))
	for paperID, pages := range paperPages {
		papers = append(papers, pdfPaper{PaperID: paperID, Pages: pages, Sections: paperSections[paperID]})
	}
	sort.Slice(papers, func(i, j int) bool { return papers[i].PaperID < papers[j].PaperID })

	result := pdfParseResult{
		Lang:              lang,
		Source:            job.OriginalName,
		PageCount:         job.PageCount,
		Papers:            papers,
		SectionPageCounts: sectionCounts,
		Blocks:            req.Blocks,
	}
	// 保留结构化试题产物，避免人工校对文本块时丢失阶段 2 结果。
	if previous != nil {
		result.Exam = previous.Exam
		result.ExamDBPath = previous.ExamDBPath
		result.OCRPPages = previous.OCRPPages
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		writeError(c, http.StatusInternalServerError, "marshal result failed")
		return
	}

	path := pdfResultPath(job)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		h.logger.Printf("write pdf result %s: %v", path, err)
		writeError(c, http.StatusInternalServerError, "保存解析结果失败")
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		h.logger.Printf("rename pdf result %s: %v", path, err)
		writeError(c, http.StatusInternalServerError, "保存解析结果失败")
		return
	}

	paperIDs := make([]string, 0, len(papers))
	for _, paper := range papers {
		paperIDs = append(paperIDs, paper.PaperID)
	}
	summary := map[string]any{
		"job_id":              job.JobID,
		"page_count":          job.PageCount,
		"block_count":         len(req.Blocks),
		"paper_count":         len(paperIDs),
		"papers":              paperIDs,
		"section_page_counts": sectionCounts,
		"result_path":         path,
		"edited_at":           time.Now().Format(time.RFC3339),
	}
	summaryRaw, _ := json.Marshal(summary)
	if err := h.store.UpdatePDFImportResult(c.Request.Context(), jobID, summaryRaw); err != nil {
		h.logger.Printf("update pdf result %s: %v", jobID, err)
	}
	_ = h.store.AppendPDFImportLog(c.Request.Context(), jobID, "info",
		fmt.Sprintf("人工校对已保存（%d 个文本块）", len(req.Blocks)))

	writeJSON(c, http.StatusOK, map[string]any{"ok": true, "summary": summary})
}

// pdfJobDelete 软删除任务：保留文件与解析结果，可在列表中恢复。
func (h *Handler) pdfJobDelete(c *gin.Context) {
	jobID := c.Param("jobId")
	job, err := h.store.GetPDFImportJob(c.Request.Context(), jobID)
	if err != nil {
		h.logger.Printf("get pdf job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "query job failed")
		return
	}
	if job == nil {
		writeError(c, http.StatusNotFound, "job not found")
		return
	}
	if err := h.store.SoftDeletePDFImportJob(c.Request.Context(), jobID); err != nil {
		h.logger.Printf("soft delete pdf job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "delete job failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// pdfJobRestore 恢复已软删除的任务。
func (h *Handler) pdfJobRestore(c *gin.Context) {
	jobID := c.Param("jobId")
	job, err := h.store.GetPDFImportJob(c.Request.Context(), jobID)
	if err != nil {
		h.logger.Printf("get pdf job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "query job failed")
		return
	}
	if job == nil {
		writeError(c, http.StatusNotFound, "job not found")
		return
	}
	if err := h.store.RestorePDFImportJob(c.Request.Context(), jobID); err != nil {
		h.logger.Printf("restore pdf job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "restore job failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// pdfJobRetry 重新投递失败或待处理的任务。
func (h *Handler) pdfJobRetry(c *gin.Context) {
	jobID := c.Param("jobId")
	job, err := h.store.GetPDFImportJob(c.Request.Context(), jobID)
	if err != nil {
		h.logger.Printf("get pdf job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "query job failed")
		return
	}
	if job == nil {
		writeError(c, http.StatusNotFound, "job not found")
		return
	}
	if job.DeletedAt != nil {
		writeError(c, http.StatusConflict, "任务已删除，请先恢复")
		return
	}
	if job.Status == "running" {
		writeError(c, http.StatusConflict, "任务正在执行，请稍后再试")
		return
	}
	if h.pdfPub == nil {
		writeError(c, http.StatusServiceUnavailable, "消息队列未配置")
		return
	}
	if err := h.store.ResetPDFImportJob(c.Request.Context(), jobID); err != nil {
		h.logger.Printf("reset pdf job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "reset job failed")
		return
	}
	if err := h.pdfPub.Publish(c.Request.Context(), map[string]string{"job_id": jobID}); err != nil {
		h.logger.Printf("republish pdf job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "消息投递失败："+err.Error())
		return
	}
	_ = h.store.MarkPDFImportQueued(c.Request.Context(), jobID)
	writeJSON(c, http.StatusAccepted, map[string]any{"job_id": jobID, "status": "queued"})
}
