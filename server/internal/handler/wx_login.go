package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// wxLoginReq 是小程序登录请求体。
// code 来自 wx.login()；nickname/avatar 可选（用户授权后才上报）。
type wxLoginReq struct {
	Code     string `json:"code"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
}

// wxUserView 是返回给小程序的用户信息（不含 openid 等敏感标识）。
type wxUserView struct {
	ID        int64  `json:"id"`
	Nickname  string `json:"nickname"`
	Avatar    string `json:"avatar"`
	IsNewUser bool   `json:"is_new_user"`
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
		"user": wxUserView{
			ID: u.ID, Nickname: u.Nickname, Avatar: u.AvatarURL, IsNewUser: isNew,
		},
	})
}

// wxMe 校验 token 并返回当前用户；无 token 或失效返回 401。
func (h *Handler) wxMe(c *gin.Context) {
	if !h.wx.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "微信登录未配置")
		return
	}
	token := bearerToken(c)
	if token == "" {
		writeError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	uid, err := h.wx.VerifyToken(token, time.Now())
	if err != nil {
		writeError(c, http.StatusUnauthorized, "login expired")
		return
	}
	u, err := h.store.GetWXUserByID(c.Request.Context(), uid)
	if err != nil {
		writeError(c, http.StatusUnauthorized, "user not found")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"user": wxUserView{ID: u.ID, Nickname: u.Nickname, Avatar: u.AvatarURL},
	})
}

// 确保 store.WXUser 类型被引用（编译期检查字段契约）。
var _ = store.WXUser{}
