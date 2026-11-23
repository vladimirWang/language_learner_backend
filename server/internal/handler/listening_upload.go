package handler

import (
	"archive/zip"
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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// listeningExamIDPattern 与 builder 侧 SAFE_ID_RE 保持一致，作为目录名。
var listeningExamIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// uploadedManifest 是素材包 manifest.json 中本接口需要的字段。
type uploadedManifest struct {
	ExamID        string            `json:"exam_id"`
	Lang          string            `json:"lang"`
	Title         string            `json:"title"`
	MediaFile     string            `json:"media_file"`
	MediaHasVideo bool              `json:"media_has_video"`
	QuestionCount int               `json:"question_count"`
	Questions     []json.RawMessage `json:"questions"`
}

// listeningUpload 接收 desktop 解析完成的听力素材 zip 包（manifest.json + 音频/图片/视频等）。
//
// multipart 字段：
//   - file      必填，zip 包；manifest.json 可在根目录或单一子目录内
//   - examId    可选，指定后必须与 manifest.exam_id 一致
//   - lang      可选，缺省取 manifest.lang
//   - level     可选，默认 A1
//   - overwrite 可选，1/true 表示 examId 目录已存在时覆盖
//
// 成功后立即把任务标记为 built，不再触发 Python 解析。
func (h *Handler) listeningUpload(c *gin.Context) {
	maxMB := envInt("LISTENING_UPLOAD_MAX_MB", 4096)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, int64(maxMB)<<20)
	if err := c.Request.ParseMultipartForm(16 << 20); err != nil {
		writeError(c, http.StatusBadRequest, "素材包过大或 multipart 无效: "+err.Error())
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		writeError(c, http.StatusBadRequest, "缺少 file（zip 素材包）")
		return
	}
	if !strings.HasSuffix(strings.ToLower(fileHeader.Filename), ".zip") {
		writeError(c, http.StatusBadRequest, "只支持 .zip 素材包")
		return
	}

	overwriteRaw := c.PostForm("overwrite")
	overwrite := overwriteRaw == "1" || strings.EqualFold(overwriteRaw, "true")

	workRoot := os.Getenv("WORK_DIR")
	if workRoot == "" {
		workRoot = "work"
	}
	jobID := fmt.Sprintf("upload-%s-%s", time.Now().Format("20060102-150405"), randomHex(4))
	uploadDir := filepath.Join(workRoot, "uploads", jobID)
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		h.logger.Printf("mkdir upload dir %s: %v", uploadDir, err)
		writeError(c, http.StatusInternalServerError, "准备上传目录失败")
		return
	}
	defer os.RemoveAll(uploadDir)

	zipPath := filepath.Join(uploadDir, "material.zip")
	src, err := fileHeader.Open()
	if err != nil {
		writeError(c, http.StatusBadRequest, "读取上传文件失败: "+err.Error())
		return
	}
	if err := saveUploadedFile(src, zipPath); err != nil {
		src.Close()
		writeError(c, http.StatusInternalServerError, "保存素材包失败: "+err.Error())
		return
	}
	src.Close()

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		writeError(c, http.StatusBadRequest, "素材包不是有效的 zip: "+err.Error())
		return
	}
	defer zr.Close()

	prefix, manifestEntry, err := locateManifest(zr.File)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	manifest, err := readUploadedManifest(manifestEntry)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}

	examID := strings.TrimSpace(manifest.ExamID)
	if want := strings.TrimSpace(c.PostForm("examId")); want != "" && want != examID {
		writeError(c, http.StatusBadRequest, "examId 与 manifest.exam_id 不一致")
		return
	}
	if !listeningExamIDPattern.MatchString(examID) {
		writeError(c, http.StatusBadRequest, "exam_id 只能包含字母、数字、下划线和中划线（1-64 位）")
		return
	}
	lang := strings.TrimSpace(c.PostForm("lang"))
	if lang == "" {
		lang = strings.TrimSpace(manifest.Lang)
	}
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "缺少或非法的 lang（如 de/en/ja）")
		return
	}
	level := strings.TrimSpace(c.PostForm("level"))
	if level == "" {
		level = "A1"
	}

	listeningRoot := filepath.Join(workRoot, "listening")
	examDir := filepath.Join(listeningRoot, examID)
	if _, statErr := os.Stat(examDir); statErr == nil && !overwrite {
		writeError(c, http.StatusConflict, "该 exam_id 已存在，确认覆盖请传 overwrite=1")
		return
	}

	stageDir := filepath.Join(listeningRoot, ".incoming-"+jobID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		h.logger.Printf("mkdir stage dir %s: %v", stageDir, err)
		writeError(c, http.StatusInternalServerError, "准备解压目录失败")
		return
	}
	defer os.RemoveAll(stageDir)

	// 解压上限按上传包大小的 4 倍兜底，避免 zip bomb 写满磁盘。
	expandLimit := int64(maxMB) << 20 * 4
	extracted, written, err := extractZipEntries(zr.File, prefix, stageDir, expandLimit)
	if err != nil {
		writeError(c, http.StatusBadRequest, "解压失败: "+err.Error())
		return
	}

	if _, statErr := os.Stat(filepath.Join(stageDir, "manifest.json")); statErr != nil {
		writeError(c, http.StatusBadRequest, "解压后缺少 manifest.json")
		return
	}
	if manifest.MediaFile != "" {
		mediaPath, joinErr := safeJoin(stageDir, manifest.MediaFile)
		if joinErr != nil {
			writeError(c, http.StatusBadRequest, "manifest.media_file 非法: "+joinErr.Error())
			return
		}
		if _, statErr := os.Stat(mediaPath); statErr != nil {
			writeError(c, http.StatusBadRequest, "manifest.media_file 指向的文件不在素材包内: "+manifest.MediaFile)
			return
		}
	}
	if len(manifest.Questions) == 0 {
		writeError(c, http.StatusBadRequest, "manifest.questions 为空，不是完整的解析结果")
		return
	}

	if err := os.MkdirAll(listeningRoot, 0o755); err != nil {
		writeError(c, http.StatusInternalServerError, "准备素材目录失败")
		return
	}
	if overwrite {
		if err := os.RemoveAll(examDir); err != nil {
			h.logger.Printf("remove old exam dir %s: %v", examDir, err)
			writeError(c, http.StatusInternalServerError, "清理旧素材目录失败")
			return
		}
	}
	if err := os.Rename(stageDir, examDir); err != nil {
		h.logger.Printf("rename %s -> %s: %v", stageDir, examDir, err)
		writeError(c, http.StatusInternalServerError, "保存素材目录失败")
		return
	}

	ctx := c.Request.Context()
	if err := h.store.CreateListeningJob(ctx, jobID, examID, lang, "upload:"+fileHeader.Filename, false); err != nil {
		h.logger.Printf("create listening job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "写入任务记录失败")
		return
	}
	hasAnswers := false
	for _, rawQuestion := range manifest.Questions {
		var q struct {
			Answer *string `json:"answer"`
		}
		if json.Unmarshal(rawQuestion, &q) == nil && q.Answer != nil && strings.TrimSpace(*q.Answer) != "" {
			hasAnswers = true
			break
		}
	}
	var name *string
	if title := strings.TrimSpace(manifest.Title); title != "" {
		name = &title
	}
	if err := h.store.UpdateListeningJobMeta(ctx, jobID, name, level); err != nil {
		h.logger.Printf("update listening job meta %s: %v", jobID, err)
	}
	manifestPath := filepath.Join(examDir, "manifest.json")
	if err := h.store.FinishListeningJob(ctx, jobID, manifestPath); err != nil {
		h.logger.Printf("finish listening job %s: %v", jobID, err)
	}
	if hasAnswers {
		if err := h.store.SetListeningJobHasAnswers(ctx, jobID); err != nil {
			h.logger.Printf("set has_answers %s: %v", jobID, err)
		}
	}
	_ = h.store.AppendListeningLog(ctx, jobID, "upload", "info",
		fmt.Sprintf("desktop 上传素材包 %s：%d 个文件，%.1f MB", fileHeader.Filename, extracted, float64(written)/1024/1024))

	writeJSON(c, http.StatusCreated, map[string]any{
		"job_id":   jobID,
		"exam_id":  examID,
		"lang":     lang,
		"level":    level,
		"status":   "built",
		"files":    extracted,
		"bytes":    written,
		"manifest": filepath.ToSlash(filepath.Join("listening", examID, "manifest.json")),
	})
}

// locateManifest 在 zip 中定位 manifest.json，返回其所在目录前缀（可能为空或 "dist/"）。
func locateManifest(files []*zip.File) (string, *zip.File, error) {
	candidates := make([]string, 0, 1)
	byName := make(map[string]*zip.File)
	for _, f := range files {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if f.FileInfo().IsDir() || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		if strings.HasSuffix(name, "manifest.json") {
			candidates = append(candidates, name)
			byName[name] = f
		}
	}
	if len(candidates) == 0 {
		return "", nil, errors.New("素材包缺少 manifest.json")
	}
	sort.Slice(candidates, func(i, j int) bool { return len(candidates[i]) < len(candidates[j]) })
	name := candidates[0]
	return strings.TrimSuffix(name, "manifest.json"), byName[name], nil
}

func readUploadedManifest(entry *zip.File) (uploadedManifest, error) {
	rc, err := entry.Open()
	if err != nil {
		return uploadedManifest{}, errors.New("读取 manifest.json 失败: " + err.Error())
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 4<<20))
	if err != nil {
		return uploadedManifest{}, errors.New("读取 manifest.json 失败: " + err.Error())
	}
	var m uploadedManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return uploadedManifest{}, errors.New("manifest.json 不是合法 JSON: " + err.Error())
	}
	return m, nil
}

// extractZipEntries 把 prefix 下的文件解压到 dest，返回文件数与总字节数。
func extractZipEntries(files []*zip.File, prefix, dest string, maxBytes int64) (int, int64, error) {
	var count int
	var total int64
	for _, f := range files {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if f.FileInfo().IsDir() || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" {
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return count, total, fmt.Errorf("不支持符号链接: %s", name)
		}
		target, err := safeJoin(dest, rel)
		if err != nil {
			return count, total, fmt.Errorf("%s: %w", name, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return count, total, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return count, total, err
		}
		rc, err := f.Open()
		if err != nil {
			out.Close()
			return count, total, err
		}
		n, copyErr := io.Copy(out, io.LimitReader(rc, maxBytes-total+1))
		rc.Close()
		closeErr := out.Close()
		total += n
		if copyErr != nil {
			return count, total, copyErr
		}
		if closeErr != nil {
			return count, total, closeErr
		}
		if total > maxBytes {
			return count, total, errors.New("解压后体积超过限制")
		}
		count++
	}
	return count, total, nil
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}
