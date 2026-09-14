package aisvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Options controls a single AI chat call from the proxy-search panel.
type Options struct {
	URL    string // OpenAI-compatible /chat/completions endpoint
	APIKey string // Bearer token
	Model  string // model id (default from endpoint)
	// Effort is off|low|medium|high|max. The old 0–10 Level is still accepted
	// by the HTTP layer and folded into this field.
	Effort    string
	PromptKey string // which prompt template to use
	// System overrides the template's system message. Without it the client
	// always used the built-in table, so prompts edited in the panel — and any
	// custom prompt_key — were silently ignored.
	System    string
	UserMsg   string // content that fills {{.Content}}
	Timeout   time.Duration
	MaxTokens int
	// AllowPrivateEndpoint permits loopback/link-local/private AI URLs. Off by
	// default: the endpoint is caller-supplied and the route is reachable from
	// the internet with an ai:write token, so a private target is an SSRF read.
	AllowPrivateEndpoint bool
}

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	Temperature     float64       `json:"temperature"`
	MaxTokens       int           `json:"max_tokens"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
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
	// The caller supplies this URL and the route is reachable without a LAN
	// gate, so an ai:write token is an SSRF read primitive: the panel dials
	// whatever host the caller names and the API key travels with it. Refuse
	// loopback / link-local / private targets unless the caller explicitly
	// opted into serving the public internet.
	if !opts.AllowPrivateEndpoint && isPrivateEndpoint(endpoint) {
		return "", fmt.Errorf("AI URL 不能指向内网/回环/链路本地地址（请在面板开启公网访问或改用公网地址）")
	}

	prompt := defaultByName(opts.PromptKey)
	if s := strings.TrimSpace(opts.System); s != "" {
		prompt.System = s
	}
	if strings.TrimSpace(prompt.User) == "" {
		// An unknown prompt_key yields an empty template, which would drop the
		// caller's content entirely.
		prompt.User = "{{.Content}}"
	}
	userMsg := strings.ReplaceAll(prompt.User, "{{.Content}}", opts.UserMsg)
	userMsg = strings.ReplaceAll(userMsg, "{{.Count}}", "50")

	ep := paramsForEffort(opts.Effort)
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = ep.MaxTokens
	}

	body := chatRequest{
		Model:           firstNonEmptyStr(opts.Model, DefaultModel),
		Messages:        []chatMessage{{Role: "system", Content: prompt.System}, {Role: "user", Content: userMsg}},
		Temperature:     ep.Temperature,
		MaxTokens:       maxTokens,
		ReasoningEffort: ep.ReasoningEffort,
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
		// Do not echo the body: it comes from a caller-chosen host and up to 300
		// bytes of any internal service's response would be handed back.
		return "", fmt.Errorf("AI endpoint status %d", resp.StatusCode)
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

// isPrivateEndpoint reports whether a URL points at the panel's own machine or
// its link-local range. Used to stop the AI-search endpoint being aimed at the
// mihomo controller, a cloud metadata service, or anything else on the LAN.
func isPrivateEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	host := u.Hostname()
	if host == "" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsPrivate() || ip.IsMulticast() || ip.IsUnspecified()
	}
	hostname := strings.ToLower(host)
	for _, name := range []string{"localhost", "metadata.google.internal"} {
		if hostname == name {
			return true
		}
	}
	// A bare name with no dots resolves against the panel's own search domains.
	return !strings.Contains(hostname, ".")
}
