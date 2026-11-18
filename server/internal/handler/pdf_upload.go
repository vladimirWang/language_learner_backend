package handler

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

var unsafeFilenamePattern = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)

// workRoot 返回构建/上传工作目录。
func workRoot() string {
	if v := strings.TrimSpace(os.Getenv("WORK_DIR")); v != "" {
		return v
	}
	return "work"
}

const pdfChunkSize = 10 << 20 // 每个分片 10MB

var pdfMD5Pattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// pdfUploadDir 返回分片临时目录。
func pdfUploadDir(uploadID string) string {
	return filepath.Join(workRoot(), "pdf", "uploads", uploadID)
}

func pdfChunkPath(uploadID string, index int) string {
	return filepath.Join(pdfUploadDir(uploadID), fmt.Sprintf("%06d.part", index))
}

// pdfUploadInit 创建或恢复分片上传会话。
func (h *Handler) pdfUploadInit(c *gin.Context) {
	var req struct {
		Filename    string `json:"filename"`
		Size        int64  `json:"size"`
		Lang        string `json:"lang"`
		Fingerprint string `json:"fingerprint"`
		MD5         string `json:"md5"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Filename = strings.TrimSpace(req.Filename)
	req.Fingerprint = strings.TrimSpace(req.Fingerprint)
	if req.Filename == "" {
		writeError(c, http.StatusBadRequest, "filename is required")
		return
	}
	if req.Size <= 0 {
		writeError(c, http.StatusBadRequest, "size must be positive")
		return
	}
	if len(req.Fingerprint) < 8 || len(req.Fingerprint) > 128 {
		writeError(c, http.StatusBadRequest, "invalid fingerprint")
		return
	}
	if req.Lang == "" {
		req.Lang = "de"
	}
	if !langPattern.MatchString(req.Lang) {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}
	req.MD5 = strings.ToLower(strings.TrimSpace(req.MD5))
	if req.MD5 != "" && !pdfMD5Pattern.MatchString(req.MD5) {
		writeError(c, http.StatusBadRequest, "invalid md5")
		return
	}

	// 同一等级下内容相同的文件直接复用已有任务，避免重复上传与解析。
	if req.MD5 != "" {
		if job, err := h.store.FindPDFImportJobByMD5(c.Request.Context(), req.MD5, req.Lang); err != nil {
			h.logger.Printf("find pdf job by md5: %v", err)
			writeError(c, http.StatusInternalServerError, "query job by md5 failed")
			return
		} else if job != nil {
			writeJSON(c, http.StatusOK, map[string]any{
				"completed":     true,
				"duplicate":     true,
				"upload_id":     "",
				"job_id":        job.JobID,
				"status":        job.Status,
				"original_name": job.OriginalName,
			})
			return
		}
	}

	existing, err := h.store.GetPDFUploadSessionByFingerprint(c.Request.Context(), req.Fingerprint)
	if err != nil {
		h.logger.Printf("get upload session: %v", err)
		writeError(c, http.StatusInternalServerError, "query upload session failed")
		return
	}
	if existing != nil {
		if existing.Status == "completed" && existing.JobID != nil {
			status := "queued"
			if job, err := h.store.GetPDFImportJob(c.Request.Context(), *existing.JobID); err == nil && job != nil {
				status = job.Status
			}
			writeJSON(c, http.StatusOK, map[string]any{
				"completed": true,
				"upload_id": existing.UploadID,
				"job_id":    *existing.JobID,
				"status":    status,
			})
			return
		}
		uploaded, err := h.store.ListPDFUploadChunks(c.Request.Context(), existing.UploadID)
		if err != nil {
			h.logger.Printf("list upload chunks: %v", err)
			writeError(c, http.StatusInternalServerError, "query upload chunks failed")
			return
		}
		writeJSON(c, http.StatusOK, map[string]any{
			"completed":       false,
			"upload_id":       existing.UploadID,
			"chunk_size":      existing.ChunkSize,
			"total_chunks":    existing.TotalChunks,
			"uploaded_chunks": uploaded,
		})
		return
	}

	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		writeError(c, http.StatusInternalServerError, "generate upload id failed")
		return
	}
	uploadID := fmt.Sprintf("up-%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(raw))
	totalChunks := int((req.Size + pdfChunkSize - 1) / pdfChunkSize)
	session := store.PDFUploadSession{
		UploadID:     uploadID,
		Fingerprint:  req.Fingerprint,
		OriginalName: req.Filename,
		SizeBytes:    req.Size,
		Lang:         req.Lang,
		ChunkSize:    pdfChunkSize,
		TotalChunks:  totalChunks,
	}
	if err := os.MkdirAll(pdfUploadDir(uploadID), 0o755); err != nil {
		h.logger.Printf("mkdir upload dir: %v", err)
		writeError(c, http.StatusInternalServerError, "prepare upload dir failed")
		return
	}
	if err := h.store.CreatePDFUploadSession(c.Request.Context(), session); err != nil {
		h.logger.Printf("create upload session: %v", err)
		writeError(c, http.StatusInternalServerError, "create upload session failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"completed":       false,
		"upload_id":       uploadID,
		"chunk_size":      pdfChunkSize,
		"total_chunks":    totalChunks,
		"uploaded_chunks": []int{},
	})
}

// pdfUploadStatus 返回会话已收到的分片，供断点续传。
func (h *Handler) pdfUploadStatus(c *gin.Context) {
	session, err := h.store.GetPDFUploadSession(c.Request.Context(), c.Param("uploadId"))
	if err != nil {
		h.logger.Printf("get upload session: %v", err)
		writeError(c, http.StatusInternalServerError, "query upload session failed")
		return
	}
	if session == nil {
		writeError(c, http.StatusNotFound, "upload session not found")
		return
	}
	if session.Status == "completed" && session.JobID != nil {
		writeJSON(c, http.StatusOK, map[string]any{
			"completed": true,
			"upload_id": session.UploadID,
			"job_id":    *session.JobID,
			"status":    "queued",
		})
		return
	}
	uploaded, err := h.store.ListPDFUploadChunks(c.Request.Context(), session.UploadID)
	if err != nil {
		h.logger.Printf("list upload chunks: %v", err)
		writeError(c, http.StatusInternalServerError, "query upload chunks failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"completed":       false,
		"upload_id":       session.UploadID,
		"chunk_size":      session.ChunkSize,
		"total_chunks":    session.TotalChunks,
		"uploaded_chunks": uploaded,
	})
}

// pdfUploadChunk 接收单个分片，重复上传覆盖同序号分片。
func (h *Handler) pdfUploadChunk(c *gin.Context) {
	uploadID := c.Param("uploadId")
	index, err := strconv.Atoi(c.Param("index"))
	if err != nil || index < 0 {
		writeError(c, http.StatusBadRequest, "invalid chunk index")
		return
	}
	session, err := h.store.GetPDFUploadSession(c.Request.Context(), uploadID)
	if err != nil {
		h.logger.Printf("get upload session: %v", err)
		writeError(c, http.StatusInternalServerError, "query upload session failed")
		return
	}
	if session == nil {
		writeError(c, http.StatusNotFound, "upload session not found")
		return
	}
	if session.Status == "completed" {
		writeError(c, http.StatusConflict, "上传已完成")
		return
	}
	if index >= session.TotalChunks {
		writeError(c, http.StatusBadRequest, "chunk index out of range")
		return
	}

	if err := os.MkdirAll(pdfUploadDir(uploadID), 0o755); err != nil {
		writeError(c, http.StatusInternalServerError, "prepare upload dir failed")
		return
	}
	path := pdfChunkPath(uploadID, index)
	tmp := path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		h.logger.Printf("create chunk %s: %v", tmp, err)
		writeError(c, http.StatusInternalServerError, "save chunk failed")
		return
	}
	size, copyErr := io.Copy(out, io.LimitReader(c.Request.Body, int64(session.ChunkSize)+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		writeError(c, http.StatusInternalServerError, "save chunk failed")
		return
	}
	if size > int64(session.ChunkSize) {
		_ = os.Remove(tmp)
		writeError(c, http.StatusBadRequest, "chunk exceeds size limit")
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		writeError(c, http.StatusInternalServerError, "save chunk failed")
		return
	}
	if err := h.store.UpsertPDFUploadChunk(c.Request.Context(), uploadID, index, size); err != nil {
		h.logger.Printf("record chunk %s/%d: %v", uploadID, index, err)
		writeError(c, http.StatusInternalServerError, "record chunk failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"received": index, "total_chunks": session.TotalChunks})
}

// mergePDFChunks 按序合并分片，并计算大小与 MD5。
func mergePDFChunks(uploadID string, session *store.PDFUploadSession, dst string) (int64, string, error) {
	out, err := os.Create(dst)
	if err != nil {
		return 0, "", err
	}
	hash := md5.New()
	var total int64
	var head []byte
	for index := 0; index < session.TotalChunks; index++ {
		path := pdfChunkPath(uploadID, index)
		f, err := os.Open(path)
		if err != nil {
			out.Close()
			return 0, "", err
		}
		if index == 0 {
			head = make([]byte, 5)
			if _, err := io.ReadFull(f, head); err != nil {
				f.Close()
				out.Close()
				return 0, "", err
			}
			if string(head) != "%PDF-" {
				f.Close()
				out.Close()
				return 0, "", errors.New("仅支持 PDF 文件")
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				f.Close()
				out.Close()
				return 0, "", err
			}
		}
		n, copyErr := io.Copy(io.MultiWriter(out, hash), f)
		f.Close()
		if copyErr != nil {
			out.Close()
			return 0, "", copyErr
		}
		total += n
	}
	if err := out.Close(); err != nil {
		return 0, "", err
	}
	return total, hex.EncodeToString(hash.Sum(nil)), nil
}

// pdfUploadComplete 合并分片、登记任务并投递解析队列。
func (h *Handler) pdfUploadComplete(c *gin.Context) {
	uploadID := c.Param("uploadId")
	session, err := h.store.GetPDFUploadSession(c.Request.Context(), uploadID)
	if err != nil {
		h.logger.Printf("get upload session: %v", err)
		writeError(c, http.StatusInternalServerError, "query upload session failed")
		return
	}
	if session == nil {
		writeError(c, http.StatusNotFound, "upload session not found")
		return
	}
	if session.Status == "completed" && session.JobID != nil {
		writeJSON(c, http.StatusOK, map[string]any{"job_id": *session.JobID, "status": "queued"})
		return
	}

	chunks, err := h.store.ListPDFUploadChunks(c.Request.Context(), uploadID)
	if err != nil {
		h.logger.Printf("list upload chunks: %v", err)
		writeError(c, http.StatusInternalServerError, "query upload chunks failed")
		return
	}
	if len(chunks) != session.TotalChunks {
		writeJSON(c, http.StatusBadRequest, map[string]any{
			"error":           "分片未上传完整",
			"received":        len(chunks),
			"total_chunks":    session.TotalChunks,
			"uploaded_chunks": chunks,
		})
		return
	}

	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		writeError(c, http.StatusInternalServerError, "generate job id failed")
		return
	}
	jobID := fmt.Sprintf("pdf-%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(raw))
	dir := filepath.Join(workRoot(), "pdf", jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.logger.Printf("mkdir pdf dir %s: %v", dir, err)
		writeError(c, http.StatusInternalServerError, "prepare job dir failed")
		return
	}
	name := unsafeFilenamePattern.ReplaceAllString(filepath.Base(session.OriginalName), "_")
	if name == "" || name == "." || name == "/" {
		name = "paper.pdf"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
		name += ".pdf"
	}
	dst := filepath.Join(dir, name)
	size, md5sum, err := mergePDFChunks(uploadID, session, dst)
	if err != nil {
		_ = os.Remove(dst)
		h.logger.Printf("merge pdf chunks %s: %v", uploadID, err)
		writeError(c, http.StatusBadRequest, "合并分片失败："+err.Error())
		return
	}
	if size != session.SizeBytes {
		_ = os.Remove(dst)
		writeError(c, http.StatusBadRequest, "文件大小校验失败，请重新上传")
		return
	}

	// 服务端再按 MD5 去重：客户端未预校验或并发上传时也复用已有任务。
	if existing, err := h.store.FindPDFImportJobByMD5(c.Request.Context(), md5sum, session.Lang); err == nil && existing != nil {
		_ = os.Remove(dst)
		_ = h.store.CompletePDFUploadSession(c.Request.Context(), uploadID, existing.JobID)
		_ = os.RemoveAll(pdfUploadDir(uploadID))
		writeJSON(c, http.StatusOK, map[string]any{
			"job_id":        existing.JobID,
			"status":        existing.Status,
			"duplicate":     true,
			"original_name": existing.OriginalName,
		})
		return
	}

	job := store.PDFImportJob{
		JobID:        jobID,
		OriginalName: session.OriginalName,
		StoredPath:   dst,
		SizeBytes:    size,
		FileMD5:      md5sum,
		Lang:         session.Lang,
		Status:       "pending",
	}
	if err := h.store.CreatePDFImportJob(c.Request.Context(), job); err != nil {
		h.logger.Printf("create pdf import job: %v", err)
		writeError(c, http.StatusInternalServerError, "create job failed")
		return
	}
	_ = h.store.CompletePDFUploadSession(c.Request.Context(), uploadID, jobID)
	_ = os.RemoveAll(pdfUploadDir(uploadID))

	status := "pending"
	warning := ""
	if h.pdfPub == nil {
		warning = "消息队列未配置，任务等待重试"
	} else {
		pubCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		if err := h.pdfPub.Publish(pubCtx, map[string]string{"job_id": jobID}); err != nil {
			h.logger.Printf("publish pdf job %s: %v", jobID, err)
			warning = "消息投递失败，可在任务列表重试"
		} else {
			_ = h.store.MarkPDFImportQueued(c.Request.Context(), jobID)
			status = "queued"
		}
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"job_id":  jobID,
		"status":  status,
		"warning": warning,
	})
}

// pdfImport 接收 PDF：先保存文件并登记数据库，再投递 RabbitMQ。
func (h *Handler) pdfImport(c *gin.Context) {
	lang := strings.TrimSpace(c.Request.FormValue("lang"))
	if lang == "" {
		lang = "de"
	}
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}

	src, header, err := c.Request.FormFile("file")
	if err != nil {
		h.logger.Printf("pdf import formfile: %v (content-type=%s, length=%d)",
			err, c.Request.Header.Get("Content-Type"), c.Request.ContentLength)
		writeError(c, http.StatusBadRequest, "file is required: "+err.Error())
		return
	}
	defer src.Close()

	magic := make([]byte, 5)
	if _, err := io.ReadFull(src, magic); err != nil || string(magic) != "%PDF-" {
		writeError(c, http.StatusBadRequest, "仅支持 PDF 文件")
		return
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		writeError(c, http.StatusBadRequest, "cannot read uploaded file")
		return
	}

	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		writeError(c, http.StatusInternalServerError, "generate job id failed")
		return
	}
	jobID := fmt.Sprintf("pdf-%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(raw))

	dir := filepath.Join(workRoot(), "pdf", jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.logger.Printf("mkdir pdf dir %s: %v", dir, err)
		writeError(c, http.StatusInternalServerError, "prepare upload dir failed")
		return
	}

	name := unsafeFilenamePattern.ReplaceAllString(filepath.Base(header.Filename), "_")
	if name == "" || name == "." || name == "/" {
		name = "paper.pdf"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
		name += ".pdf"
	}
	dst := filepath.Join(dir, name)

	out, err := os.Create(dst)
	if err != nil {
		h.logger.Printf("create pdf %s: %v", dst, err)
		writeError(c, http.StatusInternalServerError, "save pdf failed")
		return
	}
	hash := md5.New()
	size, copyErr := io.Copy(io.MultiWriter(out, hash), src)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(dst)
		h.logger.Printf("save pdf %s: %v / %v", dst, copyErr, closeErr)
		writeError(c, http.StatusInternalServerError, "save pdf failed")
		return
	}

	job := store.PDFImportJob{
		JobID:        jobID,
		OriginalName: header.Filename,
		StoredPath:   dst,
		SizeBytes:    size,
		FileMD5:      hex.EncodeToString(hash.Sum(nil)),
		Lang:         lang,
		Status:       "pending",
	}
	if err := h.store.CreatePDFImportJob(c.Request.Context(), job); err != nil {
		h.logger.Printf("create pdf import job: %v", err)
		writeError(c, http.StatusInternalServerError, "create job failed")
		return
	}

	if h.pdfPub == nil {
		writeJSON(c, http.StatusAccepted, map[string]any{
			"job_id": jobID, "status": "pending", "message": "消息队列未配置，任务等待手动重试",
		})
		return
	}
	pubCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if err := h.pdfPub.Publish(pubCtx, map[string]string{"job_id": jobID}); err != nil {
		h.logger.Printf("publish pdf job %s: %v", jobID, err)
		writeJSON(c, http.StatusInternalServerError, map[string]any{
			"job_id": jobID, "status": "pending", "error": "PDF 已保存，但消息投递失败，可在任务列表重试",
		})
		return
	}
	_ = h.store.MarkPDFImportQueued(c.Request.Context(), jobID)
	writeJSON(c, http.StatusAccepted, map[string]any{"job_id": jobID, "status": "queued"})
}
