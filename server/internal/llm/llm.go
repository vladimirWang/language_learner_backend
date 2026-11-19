// Package llm 提供最小化的 OpenAI 兼容对话客户端。
// 未配置 API Key 时 Enabled() 返回 false，调用方应回退到模板话术。
package llm

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
	"strings"
	"time"
)

// Client 是 OpenAI 兼容的 Chat Completions 客户端。
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewFromEnv 读取 LLM_BASE_URL / LLM_API_KEY / LLM_MODEL 构造客户端。
// 未显式配置 LLM_* 时回退到 DASHSCOPE_*（项目现有的大模型凭证）。
func NewFromEnv() *Client {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("LLM_BASE_URL")), "/")
	apiKey := strings.TrimSpace(os.Getenv("LLM_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY"))
		if base == "" {
			base = strings.TrimRight(strings.TrimSpace(os.Getenv("DASHSCOPE_BASE_URL")), "/")
		}
	}
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	model := strings.TrimSpace(os.Getenv("LLM_MODEL"))
	if model == "" {
		if strings.Contains(base, "dashscope") {
			model = "qwen-plus"
		} else {
			model = "gpt-4o-mini"
		}
	}
	return &Client{
		baseURL: base,
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

// Enabled 表示是否已配置可用的 API Key。
func (c *Client) Enabled() bool {
	return c != nil && c.apiKey != ""
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat 发送一次对话请求，返回模型文本。
func (c *Client) Chat(ctx context.Context, system, user string) (string, error) {
	return c.chat(ctx, system, user, 0.7, 120, false)
}

// ChatJSON 以低温度请求结构化输出，用于语法批改等需要解析 JSON 的场景。
func (c *Client) ChatJSON(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if maxTokens <= 0 || maxTokens > 2000 {
		maxTokens = 800
	}
	return c.chat(ctx, system, user, 0.2, maxTokens, true)
}

func (c *Client) chat(ctx context.Context, system, user string, temperature float64, maxTokens int, jsonMode bool) (string, error) {
	if !c.Enabled() {
		return "", errors.New("llm not configured")
	}
	payload := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: temperature,
		MaxTokens:   maxTokens,
	}
	if jsonMode {
		payload.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("decode llm response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return "", fmt.Errorf("llm http %d: %s", resp.StatusCode, parsed.Error.Message)
		}
		return "", fmt.Errorf("llm http %d", resp.StatusCode)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("llm returned no choices")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

// Transcribe 用语音识别模型把音频转写为文本。
// 走 OpenAI 兼容的 chat/completions：DashScope 的 qwen3-asr 系列以 input_audio 传入音频。
// 模型可用 ASR_MODEL 覆盖，便于切换到其他兼容实现。
// lang 指定音频语种（如 de/en/ja）；为空则由模型自动检测。
func (c *Client) Transcribe(ctx context.Context, audio []byte, mime, lang string) (string, error) {
	if !c.Enabled() {
		return "", errors.New("llm not configured")
	}
	if len(audio) == 0 {
		return "", errors.New("empty audio")
	}
	if mime == "" {
		mime = "audio/wav"
	}
	model := strings.TrimSpace(os.Getenv("ASR_MODEL"))
	if model == "" {
		model = "qwen3-asr-flash"
	}
	payload := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "input_audio",
						"input_audio": map[string]string{
							"data": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(audio),
						},
					},
				},
			},
		},
		"stream": false,
	}
	// 不指定语种时模型会自行检测，容易把外语回答识别/翻译成中文；
	// 已知语种时显式传入，保证按原语种转写。
	if lang = strings.TrimSpace(lang); lang != "" {
		payload["asr_options"] = map[string]any{"language": lang}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	// 音频上传与转写耗时远高于文本对话，单独放宽超时。
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("decode asr response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return "", fmt.Errorf("asr http %d: %s", resp.StatusCode, parsed.Error.Message)
		}
		return "", fmt.Errorf("asr http %d", resp.StatusCode)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("asr returned no choices")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}
