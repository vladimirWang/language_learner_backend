package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// SSOConfig 是管理台 SSO 登录配置；任一关键项为空时视为未启用。
type SSOConfig struct {
	// BaseURL 供服务端调用 SSO（换 token / 校验会话），容器内通常是 host.docker.internal。
	BaseURL string
	// PublicBaseURL 供浏览器跳转授权页。
	PublicBaseURL string
	ClientID      string
	ClientSecret  string
	RedirectURI   string
}

// Enabled 判断 SSO 是否可用。
func (c SSOConfig) Enabled() bool {
	return c.BaseURL != "" && c.ClientID != "" && c.ClientSecret != "" && c.RedirectURI != ""
}

// ssoUser 是管理台登录用户的最小信息。
type ssoUser struct {
	UserID   int64  `json:"user_id"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

type ssoCacheEntry struct {
	user      ssoUser
	expiresAt time.Time
}

const ssoCacheTTL = time.Minute

// ssoConfigHandler 返回前端构建授权地址所需的公开配置。
func (h *Handler) ssoConfigHandler(c *gin.Context) {
	base := strings.TrimRight(h.sso.PublicBaseURL, "/")
	writeJSON(c, http.StatusOK, map[string]any{
		"enabled":       h.sso.Enabled(),
		"authorize_url": base + "/oauth/authorize",
		"client_id":     h.sso.ClientID,
		"redirect_uri":  h.sso.RedirectURI,
	})
}

// ssoExchange 用 OAuth2 授权码换 SSO token；client_secret 只保存在服务端。
func (h *Handler) ssoExchange(c *gin.Context) {
	var req struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirect_uri"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	if !h.sso.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "sso not configured")
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		writeError(c, http.StatusBadRequest, "code is required")
		return
	}
	redirectURI := strings.TrimSpace(req.RedirectURI)
	if redirectURI == "" {
		redirectURI = h.sso.RedirectURI
	}
	if redirectURI != h.sso.RedirectURI {
		writeError(c, http.StatusBadRequest, "非法的回跳地址")
		return
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", h.sso.ClientID)
	form.Set("client_secret", h.sso.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, strings.TrimRight(h.sso.BaseURL, "/")+"/oauth/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "build sso request failed")
		return
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(httpReq)
	if err != nil {
		h.logger.Printf("sso token exchange: %v", err)
		writeError(c, http.StatusBadGateway, "SSO 服务暂不可用，请稍后重试")
		return
	}
	defer resp.Body.Close()
	var payload struct {
		AccessToken      string `json:"access_token"`
		TokenType        string `json:"token_type"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Userinfo         struct {
			UserID   int64   `json:"userId"`
			Email    string  `json:"email"`
			Username *string `json:"username"`
		} `json:"userinfo"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		h.logger.Printf("sso token exchange decode: %v", err)
		writeError(c, http.StatusBadGateway, "SSO 返回格式异常")
		return
	}
	if payload.Error != "" || payload.AccessToken == "" || payload.Userinfo.Email == "" {
		msg := payload.ErrorDescription
		if msg == "" {
			msg = "SSO 授权失败，请重新登录"
		}
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	username := ""
	if payload.Userinfo.Username != nil {
		username = *payload.Userinfo.Username
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"access_token": payload.AccessToken,
		"token_type":   payload.TokenType,
		"expires_in":   payload.ExpiresIn,
		"user": ssoUser{
			UserID:   payload.Userinfo.UserID,
			Email:    payload.Userinfo.Email,
			Username: username,
		},
	})
}

// ssoMe 校验当前 token 并返回用户信息；无 token 或会话失效返回 401。
func (h *Handler) ssoMe(c *gin.Context) {
	token := bearerToken(c)
	if token == "" {
		writeError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !h.sso.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "sso not configured")
		return
	}
	user, err := h.fetchSSOUser(c.Request.Context(), token)
	if err != nil {
		writeError(c, http.StatusUnauthorized, "sso session invalid")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"user": user})
}

// bearerToken 兼容 `Authorization: Bearer <token>` 与裸 token 两种写法。
func bearerToken(c *gin.Context) string {
	raw := strings.TrimSpace(c.GetHeader("Authorization"))
	if raw == "" {
		return ""
	}
	if len(raw) > 7 && strings.EqualFold(raw[:7], "bearer ") {
		return strings.TrimSpace(raw[7:])
	}
	return raw
}

// fetchSSOUser 调用 SSO userinfo 校验 token，并做 1 分钟进程内缓存。
func (h *Handler) fetchSSOUser(ctx context.Context, token string) (ssoUser, error) {
	now := time.Now()
	h.ssoMu.Lock()
	if entry, ok := h.ssoCache[token]; ok && now.Before(entry.expiresAt) {
		h.ssoMu.Unlock()
		return entry.user, nil
	}
	h.ssoMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, strings.TrimRight(h.sso.BaseURL, "/")+"/oauth/userinfo", nil,
	)
	if err != nil {
		return ssoUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return ssoUser{}, err
	}
	defer resp.Body.Close()
	var payload struct {
		Code int `json:"code"`
		Data struct {
			UserID   int64   `json:"userId"`
			Email    string  `json:"email"`
			Username *string `json:"username"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return ssoUser{}, err
	}
	if resp.StatusCode != http.StatusOK || payload.Code != 200 || payload.Data.Email == "" {
		return ssoUser{}, errors.New("invalid sso session")
	}
	username := ""
	if payload.Data.Username != nil {
		username = *payload.Data.Username
	}
	user := ssoUser{UserID: payload.Data.UserID, Email: payload.Data.Email, Username: username}

	h.ssoMu.Lock()
	if h.ssoCache == nil {
		h.ssoCache = make(map[string]ssoCacheEntry)
	}
	// 简单清理，避免长期运行缓存无限增长。
	if len(h.ssoCache) > 2048 {
		for key, entry := range h.ssoCache {
			if now.After(entry.expiresAt) {
				delete(h.ssoCache, key)
			}
		}
	}
	h.ssoCache[token] = ssoCacheEntry{user: user, expiresAt: now.Add(ssoCacheTTL)}
	h.ssoMu.Unlock()
	return user, nil
}
