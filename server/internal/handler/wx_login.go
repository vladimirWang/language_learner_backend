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

// wxUserView 是返回给小程序的用户信息（不含 openid 等敏感标识）。
type wxUserView struct {
	ID         int64  `json:"id"`
	Nickname   string `json:"nickname"`
	Avatar     string `json:"avatar"`
	IsNewUser  bool   `json:"is_new_user"`
	TotalStars int64  `json:"total_stars"`
}

func (h *Handler) newWXUserView(ctx context.Context, u store.WXUser, isNew bool) wxUserView {
	avatar := u.AvatarURL
	if avatar != "" && !strings.HasPrefix(avatar, "http://") && !strings.HasPrefix(avatar, "https://") {
		if h.presigner == nil {
			avatar = ""
		} else {
			signed, err := h.presigner.URL(ctx, avatar)
			if err != nil {
				h.logger.Printf("wx user avatar presign user=%d: %v", u.ID, err)
				avatar = ""
			} else {
				avatar = signed
			}
		}
	}
	return wxUserView{
		ID: u.ID, Nickname: u.Nickname, Avatar: avatar, IsNewUser: isNew, TotalStars: u.TotalStars,
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
	Nickname string `json:"nickname"`
}

// wxUpdateProfile 更新当前用户的微信昵称。
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
	nickname := strings.TrimSpace(req.Nickname)
	if nickname == "" || utf8.RuneCountInString(nickname) > 64 {
		writeError(c, http.StatusBadRequest, "nickname must contain 1 to 64 characters")
		return
	}
	updated, err := h.store.UpdateWXUserNickname(c.Request.Context(), u.ID, nickname)
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
	if h.presigner == nil {
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
	key := fmt.Sprintf("wx-avatars/%d/%s.%s", u.ID, hex.EncodeToString(random), ext)
	if err := h.presigner.Upload(c.Request.Context(), key, file, header.Size, contentType); err != nil {
		if errors.Is(err, storage.ErrNotConfigured) {
			writeError(c, http.StatusServiceUnavailable, "avatar storage not configured")
			return
		}
		h.logger.Printf("wx-avatar upload user=%d: %v", u.ID, err)
		writeError(c, http.StatusInternalServerError, "upload avatar failed")
		return
	}
	updated, err := h.store.UpdateWXUserAvatar(c.Request.Context(), u.ID, key)
	if err != nil {
		h.logger.Printf("wx-avatar save user=%d: %v", u.ID, err)
		writeError(c, http.StatusInternalServerError, "save avatar failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"user": h.newWXUserView(c.Request.Context(), updated, false)})
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
