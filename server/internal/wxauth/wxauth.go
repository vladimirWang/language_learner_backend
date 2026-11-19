// Package wxauth 实现微信小程序登录：code2session 换取 openid，以及签发/校验自包含 HMAC token。
// 不引入第三方 JWT 依赖：token 格式为 base64url(payload).base64url(sig)，签名用 HMAC-SHA256。
package wxauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const code2SessionPath = "/sns/jscode2session"

// Config 微信小程序登录配置。任一关键项为空时 Enabled 返回 false。
type Config struct {
	AppID     string
	AppSecret string
	// TokenSecret 用于签发 token 的 HMAC 密钥；为空时回退到 AppSecret（不推荐）。
	TokenSecret string
	// TokenTTL token 有效时长；<=0 时用默认 30 天。
	TokenTTL time.Duration
	// BaseURL 微信接口域名，默认 https://api.weixin.qq.com（便于测试替换）。
	BaseURL string
}

func (c Config) Enabled() bool {
	return strings.TrimSpace(c.AppID) != "" && strings.TrimSpace(c.AppSecret) != ""
}

func (c Config) secret() string {
	if strings.TrimSpace(c.TokenSecret) != "" {
		return c.TokenSecret
	}
	return c.AppSecret
}

func (c Config) ttl() time.Duration {
	if c.TokenTTL > 0 {
		return c.TokenTTL
	}
	return 30 * 24 * time.Hour
}

// TTL 返回实际生效的 token 有效时长（供接口返回 expires_in）。
func (c Config) TTL() time.Duration { return c.ttl() }

// CodeSession 是 code2session 的成功响应。
type CodeSession struct {
	OpenID     string `json:"openid"`
	UnionID    string `json:"unionid"`
	SessionKey string `json:"session_key"`
	ErrorCode  int    `json:"errcode"`
	ErrorMsg   string `json:"errmsg"`
}

// ExchangeCode 用小程序 wx.login 拿到的 code 换取 openid/session_key。
// code 只能使用一次且有效期约 5 分钟；错误统一转成可读 error。
func (c Config) ExchangeCode(ctx context.Context, code string) (CodeSession, error) {
	if !c.Enabled() {
		return CodeSession{}, errors.New("wechat login not configured")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return CodeSession{}, errors.New("code is required")
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://api.weixin.qq.com"
	}
	q := url.Values{}
	q.Set("appid", c.AppID)
	q.Set("secret", c.AppSecret)
	q.Set("js_code", code)
	q.Set("grant_type", "authorization_code")
	endpoint := base + code2SessionPath + "?" + q.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return CodeSession{}, fmt.Errorf("build code2session request: %w", err)
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return CodeSession{}, fmt.Errorf("code2session request failed: %w", err)
	}
	defer resp.Body.Close()
	var cs CodeSession
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cs); err != nil {
		return CodeSession{}, errors.New("code2session response invalid")
	}
	if cs.ErrorCode != 0 || cs.OpenID == "" {
		msg := cs.ErrorMsg
		if msg == "" {
			msg = "微信登录失败，请重试"
		}
		return CodeSession{}, fmt.Errorf("code2session: %s (errcode %d)", msg, cs.ErrorCode)
	}
	return cs, nil
}

// tokenPayload 是自包含 token 的内容。uid 为用户主键，exp 为过期 Unix 秒。
type tokenPayload struct {
	UID int64 `json:"uid"`
	Exp int64 `json:"exp"`
}

// SignToken 签发 token。
func (c Config) SignToken(uid int64, now time.Time) (string, error) {
	payload, err := json.Marshal(tokenPayload{UID: uid, Exp: now.Add(c.ttl()).Unix()})
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + c.sign(body), nil
}

// VerifyToken 校验 token 签名与过期时间，成功返回 uid。
func (c Config) VerifyToken(token string, now time.Time) (int64, error) {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, errors.New("invalid token")
	}
	if !hmac.Equal([]byte(parts[1]), []byte(c.sign(parts[0]))) {
		return 0, errors.New("invalid token signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, errors.New("invalid token payload")
	}
	var p tokenPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.UID <= 0 {
		return 0, errors.New("invalid token payload")
	}
	if now.Unix() >= p.Exp {
		return 0, errors.New("token expired")
	}
	return p.UID, nil
}

func (c Config) sign(body string) string {
	mac := hmac.New(sha256.New, []byte(c.secret()))
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
