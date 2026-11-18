// Package llm 提供最小化的 OpenAI 兼容对话客户端。
// 未配置 API Key 时 Enabled() 返回 false，调用方应回退到模板话术。
package llm

import (
	"bytes"
	"context"
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
		http:    &http.Client{Timeout: 20 * time.Second},
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
