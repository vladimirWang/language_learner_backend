package wxauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignVerifyToken(t *testing.T) {
	cfg := Config{AppID: "wx-appid", AppSecret: "wx-secret", TokenSecret: "a-long-random-token-secret"}
	now := time.Unix(1700000000, 0)
	token, err := cfg.SignToken(42, now)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	uid, err := cfg.VerifyToken(token, now.Add(time.Hour))
	if err != nil || uid != 42 {
		t.Fatalf("verify: uid=%d err=%v", uid, err)
	}
}

func TestVerifyTokenRejectsTamperExpiryAndMalformed(t *testing.T) {
	cfg := Config{AppID: "wx-appid", AppSecret: "s", TokenSecret: "secret"}
	now := time.Unix(1700000000, 0)
	token, _ := cfg.SignToken(7, now)

	ttl := cfg.TTL()

	// 过期
	if _, err := cfg.VerifyToken(token, now.Add(ttl+time.Second)); err == nil {
		t.Fatal("expected expired error")
	}
	// 篡改签名
	parts := strings.Split(token, ".")
	tampered := parts[0] + "." + strings.Repeat("A", len(parts[1]))
	if _, err := cfg.VerifyToken(tampered, now); err == nil {
		t.Fatal("expected bad signature error")
	}
	// 篡改 payload（uid）
	badBody := "eyJ1aWQiOjk5OSwiZXhwIjoxOTk5OTk5OTk5fQ"
	if _, err := cfg.VerifyToken(badBody+"."+parts[1], now); err == nil {
		t.Fatal("expected bad payload signature error")
	}
	// 格式错误
	for _, bad := range []string{"", "abc", "a.b.c", ".", "a."} {
		if _, err := cfg.VerifyToken(bad, now); err == nil {
			t.Fatalf("expected error for malformed token %q", bad)
		}
	}
}

func TestVerifyTokenSecretIsolation(t *testing.T) {
	a := Config{AppID: "x", AppSecret: "s", TokenSecret: "secret-A"}
	b := Config{AppID: "x", AppSecret: "s", TokenSecret: "secret-B"}
	token, _ := a.SignToken(1, time.Now())
	if _, err := b.VerifyToken(token, time.Now()); err == nil {
		t.Fatal("token should not verify under different secret")
	}
}

func TestExchangeCodeAgainstMockServer(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		q := r.URL.Query()
		if q.Get("appid") != "app" || q.Get("secret") != "sec" || q.Get("js_code") != "code123" || q.Get("grant_type") != "authorization_code" {
			t.Errorf("unexpected query: %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"openid":"o-abc","unionid":"u-1","session_key":"sk"}`))
	}))
	defer srv.Close()

	cfg := Config{AppID: "app", AppSecret: "sec", BaseURL: srv.URL}
	cs, err := cfg.ExchangeCode(context.Background(), "code123")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if cs.OpenID != "o-abc" || cs.UnionID != "u-1" {
		t.Fatalf("unexpected session: %+v", cs)
	}
	if gotPath != code2SessionPath {
		t.Fatalf("unexpected path: %s", gotPath)
	}
}

func TestExchangeCodeErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errcode":40029,"errmsg":"invalid code"}`))
	}))
	defer srv.Close()
	cfg := Config{AppID: "app", AppSecret: "sec", BaseURL: srv.URL}
	if _, err := cfg.ExchangeCode(context.Background(), "bad"); err == nil {
		t.Fatal("expected error for invalid code")
	}
}

func TestDisabledConfig(t *testing.T) {
	cfg := Config{}
	if cfg.Enabled() {
		t.Fatal("empty config should be disabled")
	}
	if _, err := cfg.ExchangeCode(context.Background(), "code"); err == nil {
		t.Fatal("disabled config should error")
	}
}
