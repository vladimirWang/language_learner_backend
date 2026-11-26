package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/storage"
	"github.com/wangfernando/language-learner/server/internal/store"
)

const maxWXAvatarSize = 5 << 20

// wxLoginReq 是小程序登录请求体。
// code 来自 wx.login()；nickname/avatar 可选（用户授权后才上报）。
type wxLoginReq struct {
	Code     string `json:"code"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
}

// wxUserView 是返回给小程序的当前用户会话信息。
// 不下发 id / openid / unionid 等身份标识，避免隐私字段出现在小程序端。
type wxUserView struct {
	Lang       string `json:"lang"`
	Level      string `json:"level"`
	Nickname   string `json:"nickname"`
	Avatar     string `json:"avatar"`
	IsNewUser  bool   `json:"is_new_user"`
	TotalStars int64  `json:"total_stars"`
}

func (h *Handler) newWXUserView(ctx context.Context, u store.WXUser, isNew bool) wxUserView {
	avatar := u.AvatarURL
	if avatar != "" && !strings.HasPrefix(avatar, "http://") && !strings.HasPrefix(avatar, "https://") {
		if h.presigner != nil {
			// 对象存储：把私有 key 签成临时下载链接。
			signed, err := h.presigner.URL(ctx, avatar)
			if err != nil {
				h.logger.Printf("wx user avatar presign user=%d: %v", u.ID, err)
				avatar = ""
			} else {
				avatar = signed
			}
		} else if h.localAvatar != nil {
			// 本地存储：返回同域相对路径，由小程序用 getBaseUrl() 拼接；
			// 同时兼容历史 S3 key（wx-avatars/<id>/<name>）与新的本地 key（<id>/<name>）。
			key := strings.TrimPrefix(avatar, "wx-avatars/")
			if f, _, err := h.localAvatar.Open(key); err == nil {
				f.Close()
				avatar = "/api/auth/wx-avatar/file/" + key
			} else {
				avatar = ""
			}
		} else {
			avatar = ""
		}
	}
	return wxUserView{
		Lang: h.defaultLang, Level: u.Level,
		Nickname: u.Nickname, Avatar: avatar, IsNewUser: isNew, TotalStars: u.TotalStars,
	}
}

// wxLogin 用 wx.login 的 code 换取 openid，登记用户并签发登录态 token。
// 未配置 AppID/AppSecret 时返回 503，不阻塞其他接口。
func (h *Handler) wxLogin(c *gin.Context) {
	if !h.wx.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "微信登录未配置")
		return
	}
	var req wxLoginReq
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if req.Code == "" {
		writeError(c, http.StatusBadRequest, "code is required")
		return
	}
	cs, err := h.wx.ExchangeCode(c.Request.Context(), req.Code)
	if err != nil {
		h.logger.Printf("wx-login: code2session: %v", err)
		writeError(c, http.StatusBadRequest, "微信登录失败，请重试")
		return
	}
	// 先判断是否新用户，用于返回 is_new_user 引导前端完善资料。
	_, getErr := h.store.GetWXUserByOpenID(c.Request.Context(), cs.OpenID)
	isNew := getErr != nil
	u, err := h.store.UpsertWXUser(c.Request.Context(), cs.OpenID, cs.UnionID, strings.TrimSpace(req.Nickname), strings.TrimSpace(req.Avatar))
	if err != nil {
		h.logger.Printf("wx-login: upsert user: %v", err)
		writeError(c, http.StatusInternalServerError, "登录失败，请稍后重试")
		return
	}
	token, err := h.wx.SignToken(u.ID, time.Now())
	if err != nil {
		h.logger.Printf("wx-login: sign token: %v", err)
		writeError(c, http.StatusInternalServerError, "登录失败，请稍后重试")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"token":      token,
		"token_type": "Bearer",
		"expires_in": int64(h.wx.TTL().Seconds()),
		"user":       h.newWXUserView(c.Request.Context(), u, isNew),
	})
}

// wxMe 校验 token 并返回当前用户；无 token 或失效返回 401。
func (h *Handler) wxMe(c *gin.Context) {
	u, ok := h.requireWXUser(c)
	if !ok {
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"user": h.newWXUserView(c.Request.Context(), u, false)})
}

type wxProfileReq struct {
	Nickname *string `json:"nickname"`
	Level    *string `json:"level"`
}

// wxUpdateProfile 更新当前用户的微信昵称或语言等级。
func (h *Handler) wxUpdateProfile(c *gin.Context) {
	u, ok := h.requireWXUser(c)
	if !ok {
		return
	}
	var req wxProfileReq
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Nickname == nil && req.Level == nil {
		writeError(c, http.StatusBadRequest, "nickname or level is required")
		return
	}
	if req.Nickname != nil {
		nickname := strings.TrimSpace(*req.Nickname)
		if nickname == "" || utf8.RuneCountInString(nickname) > 64 {
			writeError(c, http.StatusBadRequest, "nickname must contain 1 to 64 characters")
			return
		}
		*req.Nickname = nickname
	}
	if req.Level != nil {
		level := strings.ToUpper(strings.TrimSpace(*req.Level))
		if !challengeLevelPattern.MatchString(level) {
			writeError(c, http.StatusBadRequest, "invalid level")
			return
		}
		*req.Level = level
	}
	updated, err := h.store.UpdateWXUserProfile(c.Request.Context(), u.ID, req.Nickname, req.Level)
	if err != nil {
		h.logger.Printf("wx-profile user=%d: %v", u.ID, err)
		writeError(c, http.StatusInternalServerError, "update profile failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"user": h.newWXUserView(c.Request.Context(), updated, false)})
}

// wxUploadAvatar 校验并上传当前用户头像，数据库保存私有对象 key。
func (h *Handler) wxUploadAvatar(c *gin.Context) {
	u, ok := h.requireWXUser(c)
	if !ok {
		return
	}
	if h.presigner == nil && h.localAvatar == nil {
		writeError(c, http.StatusServiceUnavailable, "avatar storage not configured")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxWXAvatarSize+(1<<20))
	file, header, err := c.Request.FormFile("avatar")
	if err != nil {
		writeError(c, http.StatusBadRequest, "avatar file is required")
		return
	}
	defer file.Close()
	if header.Size <= 0 || header.Size > maxWXAvatarSize {
		writeError(c, http.StatusBadRequest, "avatar must be between 1 byte and 5 MiB")
		return
	}
	head := make([]byte, 512)
	n, readErr := file.Read(head)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		writeError(c, http.StatusBadRequest, "read avatar failed")
		return
	}
	contentType, ext, valid := wxAvatarFileType(head[:n])
	if !valid {
		writeError(c, http.StatusBadRequest, "avatar must be JPEG, PNG or WebP")
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(c, http.StatusBadRequest, "read avatar failed")
		return
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		h.logger.Printf("wx-avatar random user=%d: %v", u.ID, err)
		writeError(c, http.StatusInternalServerError, "upload avatar failed")
		return
	}
	randomName := hex.EncodeToString(random) + "." + ext
	var key string
	if h.presigner != nil {
		key = fmt.Sprintf("wx-avatars/%d/%s", u.ID, randomName)
		if err := h.presigner.Upload(c.Request.Context(), key, file, header.Size, contentType); err != nil {
			if errors.Is(err, storage.ErrNotConfigured) {
				writeError(c, http.StatusServiceUnavailable, "avatar storage not configured")
				return
			}
			h.logger.Printf("wx-avatar upload user=%d: %v", u.ID, err)
			writeError(c, http.StatusInternalServerError, "upload avatar failed")
			return
		}
	} else {
		// 本地存储：key 为 <userID>/<random>.<ext>，与历史 S3 key 区分（无 wx-avatars/ 前缀）。
		localKey, err := h.localAvatar.Upload(u.ID, file, ext)
		if err != nil {
			h.logger.Printf("wx-avatar local upload user=%d: %v", u.ID, err)
			writeError(c, http.StatusInternalServerError, "upload avatar failed")
			return
		}
		key = localKey
	}
	updated, err := h.store.UpdateWXUserAvatar(c.Request.Context(), u.ID, key)
	if err != nil {
		h.logger.Printf("wx-avatar save user=%d: %v", u.ID, err)
		writeError(c, http.StatusInternalServerError, "save avatar failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"user": h.newWXUserView(c.Request.Context(), updated, false)})
}

// wxAvatarFile 在未配置对象存储时按 key 回读本地头像（同域 /api/auth/wx-avatar/file/<key>）。
// 头像为公开资料且 key 不可枚举，故不需要鉴权；小程序 image 组件不能带 Authorization 头。
func (h *Handler) wxAvatarFile(c *gin.Context) {
	if h.presigner != nil || h.localAvatar == nil {
		writeError(c, http.StatusNotFound, "avatar not found")
		return
	}
	key := strings.TrimPrefix(c.Param("key"), "/")
	// 兼容历史 key：wx-avatars/<id>/<name>。
	key = strings.TrimPrefix(key, "wx-avatars/")
	f, info, err := h.localAvatar.Open(key)
	if err != nil {
		writeError(c, http.StatusNotFound, "avatar not found")
		return
	}
	defer f.Close()
	switch strings.ToLower(filepath.Ext(info.Name())) {
	case ".jpg", ".jpeg":
		c.Header("Content-Type", "image/jpeg")
	case ".png":
		c.Header("Content-Type", "image/png")
	case ".webp":
		c.Header("Content-Type", "image/webp")
	default:
		c.Header("Content-Type", "application/octet-stream")
	}
	c.Header("Cache-Control", "public, max-age=86400")
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), f)
}

func wxAvatarFileType(head []byte) (contentType, ext string, ok bool) {
	if len(head) >= 3 && head[0] == 0xff && head[1] == 0xd8 && head[2] == 0xff {
		return "image/jpeg", "jpg", true
	}
	if len(head) >= 8 && string(head[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png", "png", true
	}
	if len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP" {
		return "image/webp", "webp", true
	}
	return "", "", false
}
