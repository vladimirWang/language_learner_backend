package handler

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/scheduler"
)

// listeningImport 接收音视频 URL + 可选的 answers/cookies 文件，异步启动 builder.listen。
// multipart 字段：url(examId/lang/model) 文本 + answers/cookies 文件（可选）。
func (h *Handler) listeningImport(c *gin.Context) {
	if err := c.Request.ParseMultipartForm(50 << 20); err != nil {
		writeError(c, http.StatusBadRequest, "invalid multipart: "+err.Error())
		return
	}

	url := strings.TrimSpace(c.Request.FormValue("url"))
	examID := strings.TrimSpace(c.Request.FormValue("examId"))
	lang := strings.TrimSpace(c.Request.FormValue("lang"))
	model := strings.TrimSpace(c.Request.FormValue("model"))

	if url == "" {
		writeError(c, http.StatusBadRequest, "url is required")
		return
	}
	if examID == "" {
		writeError(c, http.StatusBadRequest, "examId is required")
		return
	}
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid or missing lang (e.g. de/en/ja)")
		return
	}
	if model == "" {
		model = "small"
	}

	// 生成 job_id
	jobIDBytes := make([]byte, 8)
	if _, err := rand.Read(jobIDBytes); err != nil {
		writeError(c, http.StatusInternalServerError, "generate job id failed")
		return
	}
	jobID := fmt.Sprintf("listen-%s-%s-%s",
		time.Now().Format("20060102-150405"), lang, hex.EncodeToString(jobIDBytes))

	// 上传文件存到 WORK_DIR/uploads/{job_id}/
	workRoot := os.Getenv("WORK_DIR")
	if workRoot == "" {
		workRoot = "work"
	}
	uploadDir := filepath.Join(workRoot, "uploads", jobID)
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		h.logger.Printf("mkdir uploads %s: %v", uploadDir, err)
		writeError(c, http.StatusInternalServerError, "prepare upload dir failed")
		return
	}

	var answersPath, cookiesPath string
	if fh, _, err := c.Request.FormFile("answers"); err == nil {
		dst := filepath.Join(uploadDir, "answers.csv")
		if err := saveUploadedFile(fh, dst); err != nil {
			fh.Close()
			writeError(c, http.StatusInternalServerError, "save answers failed: "+err.Error())
			return
		}
		fh.Close()
		answersPath = dst
	}
	if fh, _, err := c.Request.FormFile("cookies"); err == nil {
		dst := filepath.Join(uploadDir, "cookies.txt")
		if err := saveUploadedFile(fh, dst); err != nil {
			fh.Close()
			writeError(c, http.StatusInternalServerError, "save cookies failed: "+err.Error())
			return
		}
		fh.Close()
		cookiesPath = dst
	}

	// 入库 pending
	hasAnswers := answersPath != ""
	if err := h.store.CreateListeningJob(c.Request.Context(), jobID, examID, lang, url, hasAnswers); err != nil {
		h.logger.Printf("create listening job: %v", err)
		writeError(c, http.StatusInternalServerError, "create job failed")
		return
	}

	// 构造 builder.listen args（Runner.cmd = "uv"，RunCustom 直接用 args）
	args := []string{
		"run", "--no-sync", "python", "-m", "builder.listen",
		"--url", url,
		"--exam-id", examID,
		"--lang", lang,
		"--target-lang", lang,
		"--model", model,
	}
	if answersPath != "" {
		args = append(args, "--answers", answersPath)
	}
	if cookiesPath != "" {
		args = append(args, "--cookies", cookiesPath)
	}

	// 触发调度。限制 Python 数值库的线程数：2C 机器上 whisper/OpenBLAS 默认会吃满
	// 所有核心，导致同容器的 Go API 和 PostgreSQL 被 CPU 饿死、请求卡住。
	err := h.sched.TriggerListening(jobID, examID, lang, args, map[string]string{
		"OMP_NUM_THREADS":      "1",
		"MKL_NUM_THREADS":      "1",
		"OPENBLAS_NUM_THREADS": "1",
		"NUMEXPR_NUM_THREADS":  "1",
	})
	switch {
	case errors.Is(err, scheduler.ErrBusy):
		writeError(c, http.StatusConflict, err.Error())
		return
	case errors.Is(err, scheduler.ErrLowMemory):
		writeError(c, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		h.logger.Printf("trigger listening: %v", err)
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(c, http.StatusAccepted, map[string]any{
		"job_id":  jobID,
		"exam_id": examID,
		"lang":    lang,
		"status":  "pending",
	})
}

func (h *Handler) listeningJobs(c *gin.Context) {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		if n, err := fmt.Sscanf(raw, "%d", &limit); n == 1 && err == nil && limit > 0 && limit <= 500 {
			// ok
		} else {
			writeError(c, http.StatusBadRequest, "limit must be 1..500")
			return
		}
	}
	jobs, err := h.store.ListListeningJobs(c.Request.Context(), limit)
	if err != nil {
		h.logger.Printf("list listening jobs: %v", err)
		writeError(c, http.StatusInternalServerError, "query jobs failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"jobs": jobs})
}

func (h *Handler) listeningJobDetail(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "missing jobId")
		return
	}
	afterLogID, _ := strconv.ParseInt(c.Query("after_log_id"), 10, 64)
	job, logs, err := h.store.ListeningJobDetail(c.Request.Context(), jobID, afterLogID)
	if err != nil {
		h.logger.Printf("listening job detail %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "query job failed")
		return
	}
	if job == nil {
		writeError(c, http.StatusNotFound, "job not found: "+jobID)
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"job":  job,
		"logs": logs,
	})
}

// listeningFile 是导入任务产物的一个可下载文件。
type listeningFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

var errListeningJobNotFound = errors.New("listening job not found")

// listeningExamDir 定位任务产物目录：WORK_DIR/listening/{exam_id}。
func (h *Handler) listeningExamDir(ctx context.Context, jobID string) (string, string, error) {
	job, _, err := h.store.ListeningJobDetail(ctx, jobID, 0)
	if err != nil {
		return "", "", err
	}
	if job == nil {
		return "", "", errListeningJobNotFound
	}
	workRoot := os.Getenv("WORK_DIR")
	if workRoot == "" {
		workRoot = "work"
	}
	return filepath.Join(workRoot, "listening", job.ExamID), job.ExamID, nil
}

// listeningFiles 列出该任务目录下的全部产物（视频、音频、图片、转写文件等）。
func (h *Handler) listeningFiles(c *gin.Context) {
	dir, examID, err := h.listeningExamDir(c.Request.Context(), c.Param("jobId"))
	switch {
	case errors.Is(err, errListeningJobNotFound):
		writeError(c, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("locate listening dir: %v", err)
		writeError(c, http.StatusInternalServerError, "locate exam dir failed")
		return
	}

	files := make([]listeningFile, 0, 32)

	// zip 不在解析阶段预生成，也不在列表接口顺手生成——否则打开一次详情页
	// 就会把整个任务目录（含原视频）再复制一份。用户点「打包下载」时再由
	// listeningDownload 按需生成。

	walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		// 排除转写中间产物（16k wav，体积大且无下载价值）
		if strings.HasSuffix(path, "source16k.wav") {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		files = append(files, listeningFile{
			Path: filepath.ToSlash(rel),
			Name: entry.Name(),
			Size: info.Size(),
		})
		return nil
	})
	if walkErr != nil {
		h.logger.Printf("walk listening dir %s: %v", dir, walkErr)
	}

	writeJSON(c, http.StatusOK, map[string]any{
		"exam_id": examID,
		"files":   files,
	})
}

// listeningDownload 下载单个产物文件，path 必须是任务目录内的相对路径。
func (h *Handler) listeningDownload(c *gin.Context) {
	dir, _, err := h.listeningExamDir(c.Request.Context(), c.Param("jobId"))
	switch {
	case errors.Is(err, errListeningJobNotFound):
		writeError(c, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("locate listening dir: %v", err)
		writeError(c, http.StatusInternalServerError, "locate exam dir failed")
		return
	}

	target, err := safeJoin(dir, c.Query("path"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}

	// 如果请求的是 zip 包，确保它已完整生成（原子 rename，不会读到半成品）。
	// 这样用户无论何时点下载，都能拿到完整的 zip。
	if strings.HasSuffix(target, ".zip") {
		if _, statErr := os.Stat(target); statErr != nil {
			if buildErr := buildListeningZip(dir, target); buildErr != nil {
				h.logger.Printf("build listening zip on download failed: %v", buildErr)
				writeError(c, http.StatusInternalServerError, "build zip failed")
				return
			}
		}
	}

	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		writeError(c, http.StatusNotFound, "file not found")
		return
	}

	c.Header("Content-Type", contentTypeFor(target))
	if isInlinePlayable(target) {
		// 音视频、图片、文本等可在浏览器内联展示，方便管理后台直接试听/预览。
		c.Header("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(target)))
	} else {
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(target)))
	}
	c.Header("X-Accel-Buffering", "no")
	http.ServeFile(c.Writer, c.Request, target)
}

// contentTypeFor 根据扩展名返回合适的 MIME type，容器里 mime.types 可能不全，
// 对常见音视频/图片扩展名做显式映射。
func contentTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4a":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".txt", ".srt", ".vtt":
		return "text/plain; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	default:
		if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
			return t
		}
		return "application/octet-stream"
	}
}

// isInlinePlayable 判断文件是否适合在浏览器内联展示（试听/预览）。
func isInlinePlayable(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4a", ".mp3", ".wav", ".ogg", ".oga", ".flac",
		".mp4", ".webm", ".mov",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg",
		".txt", ".srt", ".vtt", ".json":
		return true
	default:
		return false
	}
}

// listeningArchive 把整个任务目录打包成 zip 下载（视频 + 音频 + 图片 + 转写）。
// 策略：首次请求时把 zip 预生成到任务目录下并持久化（不删除），之后直接 ServeFile。
// 这样 zip 下载与单文件下载走完全相同的 ServeFile 路径，避免流式打包导致的下载卡住。
func (h *Handler) listeningArchive(c *gin.Context) {
	dir, examID, err := h.listeningExamDir(c.Request.Context(), c.Param("jobId"))
	switch {
	case errors.Is(err, errListeningJobNotFound):
		writeError(c, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("locate listening dir: %v", err)
		writeError(c, http.StatusInternalServerError, "locate exam dir failed")
		return
	}
	if _, err := os.Stat(dir); err != nil {
		writeError(c, http.StatusNotFound, "exam output not found")
		return
	}

	zipPath := filepath.Join(dir, examID+".zip")

	// 缓存不存在则生成（用 Store 不压缩，速度快）。生成到磁盘文件而非 response，
	// 生成完成后走 ServeFile，与单文件下载路径一致，稳定支持大文件下载。
	if _, statErr := os.Stat(zipPath); statErr != nil {
		if err := buildListeningZip(dir, zipPath); err != nil {
			h.logger.Printf("build listening zip failed: %v", err)
			writeError(c, http.StatusInternalServerError, "build zip failed")
			return
		}
	}

	// 告诉 nginx 等反向代理不要缓冲此响应，否则大文件下载会卡在代理缓冲阶段。
	c.Header("X-Accel-Buffering", "no")
	http.ServeFile(c.Writer, c.Request, zipPath)
}

// buildListeningZip 把 dir 下的文件（排除 source16k.wav）用 Store 模式打包到 zipPath。
// 先写到临时文件，完成后 rename 到目标路径（原子操作），避免下载时读到半成品。
func buildListeningZip(dir, zipPath string) error {
	tmp, err := os.CreateTemp(filepath.Dir(zipPath), ".listening-*.zip.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// 清理临时文件（成功 rename 后不影响，失败时兜底）
	defer os.Remove(tmpPath)

	zw := zip.NewWriter(tmp)
	walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		// 排除自身 zip 与转写中间产物
		if path == zipPath || strings.HasSuffix(path, "source16k.wav") {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		src, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer src.Close()
		dst, createErr := zw.CreateHeader(&zip.FileHeader{
			Name:   filepath.ToSlash(rel),
			Method: zip.Store,
		})
		if createErr != nil {
			return nil
		}
		_, _ = io.Copy(dst, src)
		return nil
	})
	if closeErr := zw.Close(); closeErr != nil {
		tmp.Close()
		return closeErr
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if walkErr != nil {
		return walkErr
	}
	// 原子替换：rename 保证下载请求要么读到旧的完整文件，要么读到新的完整文件。
	return os.Rename(tmpPath, zipPath)
}

// safeJoin 把相对路径安全地拼到 root 下，拒绝越界与绝对路径。
func safeJoin(root, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" {
		return "", errors.New("missing path")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("invalid path")
	}
	rootClean := filepath.Clean(root)
	full := filepath.Join(rootClean, clean)
	if full != rootClean && !strings.HasPrefix(full, rootClean+string(os.PathSeparator)) {
		return "", errors.New("path escapes exam dir")
	}
	return full, nil
}

func saveUploadedFile(src io.ReadCloser, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, src); err != nil {
		return err
	}
	return nil
}

// listeningJobAnswers 返回任务 manifest 中已绑定的答案，供管理台回显。
func (h *Handler) listeningJobAnswers(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "missing jobId")
		return
	}
	dir, _, err := h.listeningExamDir(c.Request.Context(), jobID)
	switch {
	case err == errListeningJobNotFound:
		writeError(c, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("listening exam dir: %v", err)
		writeError(c, http.StatusInternalServerError, "locate exam dir failed")
		return
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		writeError(c, http.StatusNotFound, "manifest not found")
		return
	}
	var manifest struct {
		Questions []struct {
			Number int `json:"number"`
			Answer any `json:"answer"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		writeError(c, http.StatusInternalServerError, "manifest parse failed")
		return
	}
	type qBrief struct {
		Number int    `json:"number"`
		Answer string `json:"answer"`
	}
	out := make([]qBrief, 0, len(manifest.Questions))
	for _, q := range manifest.Questions {
		if answer, ok := q.Answer.(string); ok && strings.TrimSpace(answer) != "" {
			out = append(out, qBrief{Number: q.Number, Answer: answer})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	writeJSON(c, http.StatusOK, map[string]any{"answers": out})
}

// listeningUpdateAnswers 允许管理员在任务完成后补充答案 CSV。
// CSV 格式：题号,答案（选择 A/B/C/D；判断 richtig/falsch）。更新 manifest.json 中对应题目的 answer 字段。
// 重新上传视为覆盖：文件中的答案是唯一答案，未出现在文件里的旧答案会被清空。
func (h *Handler) listeningUpdateAnswers(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "missing jobId")
		return
	}
	if err := c.Request.ParseMultipartForm(10 << 20); err != nil {
		writeError(c, http.StatusBadRequest, "invalid multipart: "+err.Error())
		return
	}
	file, _, err := c.Request.FormFile("answers")
	if err != nil {
		writeError(c, http.StatusBadRequest, "missing answers.csv")
		return
	}
	defer file.Close()

	answerMap, err := parseAnswerCSV(file)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	if len(answerMap) == 0 {
		writeError(c, http.StatusBadRequest, "答案文件里没有解析到任何题号")
		return
	}

	dir, _, err := h.listeningExamDir(c.Request.Context(), jobID)
	switch {
	case err == errListeningJobNotFound:
		writeError(c, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("listening exam dir: %v", err)
		writeError(c, http.StatusInternalServerError, "locate exam dir failed")
		return
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "manifest not found")
		return
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		writeError(c, http.StatusInternalServerError, "manifest parse failed")
		return
	}
	questions, ok := manifest["questions"].([]any)
	if !ok {
		writeError(c, http.StatusInternalServerError, "manifest questions invalid")
		return
	}

	updated := 0
	for _, q := range questions {
		qMap, ok := q.(map[string]any)
		if !ok {
			continue
		}
		// 重新上传视为覆盖：先清空旧答案，再写入本次文件里的答案。
		qMap["answer"] = nil
		num, ok := qMap["number"].(float64)
		if !ok {
			continue
		}
		if ans, exists := answerMap[int(num)]; exists {
			qMap["answer"] = ans
			updated++
		}
	}
	manifest["questions"] = questions

	out, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		writeError(c, http.StatusInternalServerError, "manifest encode failed")
		return
	}
	if err := os.WriteFile(manifestPath, out, 0o644); err != nil {
		writeError(c, http.StatusInternalServerError, "manifest write failed")
		return
	}

	type qBrief struct {
		Number int    `json:"number"`
		Answer string `json:"answer"`
	}
	updatedList := make([]qBrief, 0, len(answerMap))
	for n := range answerMap {
		updatedList = append(updatedList, qBrief{Number: n, Answer: answerMap[n]})
	}
	sort.Slice(updatedList, func(i, j int) bool { return updatedList[i].Number < updatedList[j].Number })

	// 上传答案后标记该素材已绑定答案
	if err := h.store.SetListeningJobHasAnswers(c.Request.Context(), jobID); err != nil {
		h.logger.Printf("set has_answers for job %s: %v", jobID, err)
	}

	writeJSON(c, http.StatusOK, map[string]any{
		"updated_count": updated,
		"answers":       updatedList,
	})
}

// updateListeningJobMeta 更新听力任务的显示名称与等级；空名称表示恢复使用 Exam ID。
func (h *Handler) updateListeningJobMeta(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "missing jobId")
		return
	}
	var req struct {
		Name  string `json:"name"`
		Level string `json:"level"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	var name *string
	if req.Name != "" {
		name = &req.Name
	}
	if req.Level != "" && !validCefrLevels[req.Level] {
		writeError(c, http.StatusBadRequest, "invalid level (A1/A2/B1/B2/C1/C2)")
		return
	}
	if err := h.store.UpdateListeningJobMeta(c.Request.Context(), jobID, name, req.Level); err != nil {
		h.logger.Printf("update listening job meta %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "update job failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// deleteListeningJob 软删除听力任务（标记 deleted_at，不删除产物文件）。
func (h *Handler) deleteListeningJob(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "missing jobId")
		return
	}
	if err := h.store.SoftDeleteListeningJob(c.Request.Context(), jobID); err != nil {
		h.logger.Printf("soft delete listening job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "delete job failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// parseAnswerCSV 解析答案 CSV：题号,答案。
// 答案支持：选择题 A/B/C/D；判断题 richtig/falsch（或缩写 r/f、true/false）。
// 归一化后存储：A/B/C/D（选择）、R（正确）/F（错误）。忽略表头与非法行。
func parseAnswerCSV(r io.Reader) (map[int]string, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("CSV 解析失败：%w", err)
	}
	result := make(map[int]string)
	for _, row := range rows {
		cells := make([]string, 0, len(row))
		for _, c := range row {
			c = strings.TrimSpace(c)
			if c != "" {
				cells = append(cells, c)
			}
		}
		if len(cells) < 2 {
			continue
		}
		numStr := strings.TrimLeft(cells[0], "qQ")
		num, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		answer, err := normalizeAnswer(cells[1])
		if err != nil {
			return nil, fmt.Errorf("第 %d 题 %w", num, err)
		}
		result[num] = answer
	}
	return result, nil
}

// normalizeAnswer 将各种答案写法归一化为标准单字符：
// 选择题 → A/B/C/D；判断题 → R（正确）/ F（错误）。
func normalizeAnswer(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "a":
		return "A", nil
	case "b":
		return "B", nil
	case "c":
		return "C", nil
	case "d":
		return "D", nil
	case "r", "richtig", "true", "t":
		return "R", nil
	case "f", "falsch", "false":
		return "F", nil
	default:
		return "", fmt.Errorf("不支持的答案 %q（支持 A/B/C/D 或 richtig/falsch）", raw)
	}
}
