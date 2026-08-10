package aisvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Options controls a single AI chat call from the proxy-search panel.
type Options struct {
	URL       string // OpenAI-compatible /chat/completions endpoint
	APIKey    string // Bearer token
	Model     string // model id (default from endpoint)
	Level     int    // 0..10 reasoning depth -> influences temperature/max_tokens
	PromptKey string // which prompt template to use
	UserMsg   string // content that fills {{.Content}}
	Timeout   time.Duration
	MaxTokens int
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// DefaultModel is used when Options.Model is empty. Common OpenAI-compatible
// gateways accept an arbitrary model string; we try a widely-supported one.
const DefaultModel = "gpt-4o-mini"

// Call sends a chat completion request with the selected prompt and returns
// the assistant's raw text answer.
func Call(ctx context.Context, opts Options) (string, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	endpoint := strings.TrimSpace(opts.URL)
	if endpoint == "" {
		return "", fmt.Errorf("AI URL 不能为空")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return "", fmt.Errorf("AI URL 必须是 http(s):// 开头")
	}

	prompt := defaultByName(opts.PromptKey)
	userMsg := strings.ReplaceAll(prompt.User, "{{.Content}}", opts.UserMsg)
	userMsg = strings.ReplaceAll(userMsg, "{{.Count}}", "50")

	level := opts.Level
	if level < 0 {
		level = 0
	}
	if level > 10 {
		level = 10
	}
	// depth dial: higher = more thorough (lower temp, more tokens)
	temperature := 0.9 - float64(level)*0.05
	if temperature < 0.2 {
		temperature = 0.2
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 800 + level*300 // 800..3800
	}
	if level >= 8 {
		maxTokens += 1500
	}

	body := chatRequest{
		Model:       firstNonEmptyStr(opts.Model, DefaultModel),
		Messages:    []chatMessage{{Role: "system", Content: prompt.System}, {Role: "user", Content: userMsg}},
		Temperature: temperature,
		MaxTokens:   maxTokens,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal chat body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(opts.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(opts.APIKey))
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("call AI endpoint: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("read AI response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("AI endpoint status %d: %s", resp.StatusCode, msg)
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("parse AI response: %w", err)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("AI error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("AI 返回空 choices")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
