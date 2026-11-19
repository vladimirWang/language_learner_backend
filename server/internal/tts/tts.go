// Package tts 封装 DashScope 非实时语音合成，供口语考官/搭档文本朗读使用。
// 走百炼原生 HTTP 端点（非 OpenAI 兼容模式），复用现有 DASHSCOPE_API_KEY。
// 合成结果按文本缓存于内存，避免同一句话重复计费。
package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL = "https://dashscope.aliyuncs.com/api/v1"
	defaultModel   = "qwen-audio-3.1-tts-flash"
	defaultVoice   = "longanlingxin_v3.1"
	defaultFormat  = "mp3"
	defaultRate    = 24000

	defaultCacheTTL   = 24 * time.Hour
	defaultCacheLimit = 200
)

// Client 是 DashScope 非实时 TTS 客户端。
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	voice      string
	format     string
	sampleRate int
	http       *http.Client

	mu      sync.Mutex
	cache   map[string]cacheEntry
	ttl     time.Duration
	limit   int
	order   []string
}

type cacheEntry struct {
	data    []byte
	created time.Time
}

// NewFromEnv 读取配置构造客户端。未配置 API Key 时 Enabled() 返回 false。
// 凭证优先复用 DASHSCOPE_API_KEY（与 LLM/ASR 相同）。
func NewFromEnv() *Client {
	apiKey := strings.TrimSpace(os.Getenv("TTS_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY"))
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("TTS_BASE_URL")), "/")
	if base == "" {
		// 若配置的是 compatible-mode/v1，去掉兼容模式段以得到原生 API 根。
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("DASHSCOPE_BASE_URL")), "/")
		base = strings.TrimSuffix(base, "/compatible-mode/v1")
		base = strings.TrimSuffix(base, "/compatible-mode")
	}
	if base == "" || !strings.HasSuffix(base, "/api/v1") {
		base = defaultBaseURL
	}
	model := strings.TrimSpace(os.Getenv("TTS_MODEL"))
	if model == "" {
		model = defaultModel
	}
	voice := strings.TrimSpace(os.Getenv("TTS_VOICE"))
	if voice == "" {
		voice = defaultVoice
	}
	format := strings.ToLower(strings.TrimSpace(os.Getenv("TTS_FORMAT")))
	if format != "mp3" && format != "wav" {
		format = defaultFormat
	}
	rate := defaultRate
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("TTS_SAMPLE_RATE"))); err == nil && n > 0 {
		rate = n
	}
	limit := defaultCacheLimit
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("TTS_CACHE_LIMIT"))); err == nil && n > 0 {
		limit = n
	}
	return &Client{
		baseURL: base, apiKey: apiKey, model: model, voice: voice,
		format: format, sampleRate: rate,
		http:  &http.Client{Timeout: 40 * time.Second},
		cache: make(map[string]cacheEntry), ttl: defaultCacheTTL, limit: limit,
	}
}

// Enabled 表示是否配置了可用凭证。
func (c *Client) Enabled() bool {
	return c != nil && c.apiKey != ""
}

type synthRequest struct {
	Model string `json:"model"`
	Input struct {
		Text          string   `json:"text"`
		Voice         string   `json:"voice"`
		Format        string   `json:"format"`
		SampleRate    int      `json:"sample_rate"`
		LanguageHints []string `json:"language_hints,omitempty"`
	} `json:"input"`
}

type synthResponse struct {
	Output struct {
		Audio struct {
			URL  string `json:"url"`
			Data string `json:"data"`
		} `json:"audio"`
		FinishReason string `json:"finish_reason"`
	} `json:"output"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Synthesize 合成文本为音频，返回音频字节与 MIME 类型。命中缓存时直接返回。
func (c *Client) Synthesize(ctx context.Context, text, lang string) ([]byte, string, error) {
	if !c.Enabled() {
		return nil, "", errors.New("tts not configured")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, "", errors.New("empty text")
	}
	lang = strings.ToLower(strings.TrimSpace(lang))
	key := lang + "|" + text
	if data, ok := c.cacheGet(key); ok {
		return data, c.mime(), nil
	}

	payload := synthRequest{Model: c.model}
	payload.Input.Text = text
	payload.Input.Voice = c.voice
	payload.Input.Format = c.format
	payload.Input.SampleRate = c.sampleRate
	// 仅对已知小语种显式指定，中文/英文无需设置（模型默认处理更好）。
	if lang == "de" {
		payload.Input.LanguageHints = []string{"de"}
	} else if lang != "" && lang != "zh" && lang != "en" {
		payload.Input.LanguageHints = []string{lang}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/services/audio/tts/SpeechSynthesizer", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if readErr != nil {
		return nil, "", readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("tts http %d: %s", resp.StatusCode, string(raw))
	}
	var parsed synthResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, "", fmt.Errorf("decode tts response: %w", err)
	}
	if parsed.Code != "" {
		return nil, "", fmt.Errorf("tts error %s: %s", parsed.Code, parsed.Message)
	}

	// 优先使用内联 base64；否则下载临时 URL。
	if parsed.Output.Audio.Data != "" {
		data, err := decodeBase64Lenient(parsed.Output.Audio.Data)
		if err != nil {
			return nil, "", err
		}
		c.cachePut(key, data)
		return data, c.mime(), nil
	}
	url := strings.TrimSpace(parsed.Output.Audio.URL)
	if url == "" {
		return nil, "", errors.New("tts returned no audio")
	}
	data, err := c.download(ctx, url)
	if err != nil {
		return nil, "", err
	}
	c.cachePut(key, data)
	return data, c.mime(), nil
}

func (c *Client) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download audio http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("downloaded audio is empty")
	}
	return data, nil
}

func (c *Client) mime() string {
	if c.format == "wav" {
		return "audio/wav"
	}
	return "audio/mpeg"
}

func decodeBase64Lenient(s string) ([]byte, error) {
	// data:...;base64, 前缀时去掉 header。
	if idx := strings.Index(s, ","); idx >= 0 && strings.HasPrefix(s, "data:") {
		s = s[idx+1:]
	}
	return base64.StdEncoding.DecodeString(s)
}

func (c *Client) cacheGet(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[key]
	if !ok {
		return nil, false
	}
	if time.Since(entry.created) > c.ttl {
		delete(c.cache, key)
		c.removeOrder(key)
		return nil, false
	}
	return entry.data, true
}

func (c *Client) cachePut(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.cache[key]; !exists {
		c.order = append(c.order, key)
	}
	c.cache[key] = cacheEntry{data: data, created: time.Now()}
	for len(c.order) > c.limit {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.cache, oldest)
	}
}

func (c *Client) removeOrder(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}
