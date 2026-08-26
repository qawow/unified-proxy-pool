package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"unified-proxy-pool/internal/aisvc"
	"unified-proxy-pool/internal/freproxies"
)

// handleAIPromptsList returns all prompt templates (built-in + customized).
func (a *App) handleAIPromptsList(w http.ResponseWriter, r *http.Request) {
	if a.prompts == nil {
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: aisvc.NewPromptStore().List()})
		return
	}
	writeList(w, a.prompts.List())
}

// handleAIPromptUpsert saves an edited prompt (system/user text).
func (a *App) handleAIPromptUpsert(w http.ResponseWriter, r *http.Request) {
	if a.prompts == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "prompts unavailable"})
		return
	}
	var p aisvc.Prompt
	if !decodeJSON(w, r, &p) {
		return
	}
	if strings.TrimSpace(p.Name) == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "prompt name required"})
		return
	}
	if strings.TrimSpace(p.System) == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "system prompt required"})
		return
	}
	a.prompts.Upsert(p)
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "ai.prompt.upsert", clientIP(r), map[string]any{"name": p.Name})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: p})
}

// handleAIPromptDelete resets a built-in prompt to default or removes a custom one.
func (a *App) handleAIPromptDelete(w http.ResponseWriter, r *http.Request) {
	if a.prompts == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "prompts unavailable"})
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "name required"})
		return
	}
	if !a.prompts.Delete(name) {
		writeJSON(w, http.StatusNotFound, apiResponse{Success: false, Message: "prompt not found"})
		return
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "ai.prompt.delete", clientIP(r), map[string]any{"name": name})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]bool{"deleted": true}})
}

// handleAISearch calls the configured AI endpoint (URL + API key + thinking
// level) with the selected prompt, parses the returned JSON, and returns the
// extracted proxy candidates WITHOUT ingesting them yet.
func (a *App) handleAISearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL       string `json:"url"`
		APIKey    string `json:"apikey"`
		Model     string `json:"model"`
		Effort    string `json:"effort"`
		Level     any    `json:"level"`
		PromptKey string `json:"prompt_key"`
		Prompt    string `json:"prompt"` // inline custom system prompt (optional)
		Content   string `json:"content"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Success: false, Message: "AI URL 不能为空"})
		return
	}

	// inline prompt overrides template
	system := strings.TrimSpace(req.Prompt)
	userMsg := strings.TrimSpace(req.Content)
	if system == "" {
		system = aisvc.DefaultSystem(firstNonEmpty(req.PromptKey, "proxy_extract"))
		if userMsg == "" {
			userMsg = "请从下面的内容中提取代理地址：\n" + strings.TrimSpace(req.Content)
		}
	}

	// Default content when none provided: instruct the model to use its own knowledge.
	if userMsg == "" {
		userMsg = "请生成常见免费代理候选地址（host:port 或 protocol://host:port），返回 JSON 数组。"
	}

	answer, err := aisvc.Call(r.Context(), aisvc.Options{
		URL:       req.URL,
		APIKey:    req.APIKey,
		Model:     req.Model,
		Effort:    resolveEffort(req.Effort, req.Level),
		PromptKey: req.PromptKey,
		UserMsg:   userMsg,
		Timeout:   90 * time.Second,
	})
	if err != nil {
		writeError(w, err)
		return
	}

	lines := extractJSONArrayLines(answer)
	proxies := make([]freproxies.Proxy, 0, len(lines))
	for _, s := range lines {
		if p := parseAddrLine(s); p.Host != "" {
			proxies = append(proxies, p)
		}
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), "admin", "ai.search", clientIP(r), map[string]any{
			"url": req.URL, "effort": resolveEffort(req.Effort, req.Level), "found": len(proxies),
		})
	}
	writeJSON(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"raw":     answer,
		"proxies": proxies,
		"count":   len(proxies),
	}})
}

// extractJSONArrayLines pulls a JSON array of strings from an AI answer that
// may include markdown fences or surrounding prose.
func extractJSONArrayLines(answer string) []string {
	text := strings.TrimSpace(answer)
	// strip ```json ... ```
	if i := strings.Index(text, "["); i >= 0 {
		text = text[i:]
		if j := strings.LastIndex(text, "]"); j >= 0 && j > i {
			text = text[:j+1]
		}
	}
	var arr []string
	if err := json.Unmarshal([]byte(text), &arr); err == nil {
		return arr
	}
	// fall back: any {proxies:[...]} wrapper
	var obj struct {
		Proxies []string `json:"proxies"`
	}
	if err := json.Unmarshal([]byte(text), &obj); err == nil {
		return obj.Proxies
	}
	return nil
}

// web package helpers used above are defined elsewhere; this stub is here only
// to keep the file self-contained for documentation purposes.
var _ = context.Background
var _ = io.Discard

// resolveEffort prefers the portable effort string. A leftover numeric
// `level` (0–10) or a name stuffed into that field still works.
func resolveEffort(effort string, level any) string {
	if s := strings.TrimSpace(effort); s != "" {
		return aisvc.NormalizeEffort(s)
	}
	switch v := level.(type) {
	case string:
		return aisvc.NormalizeEffort(v)
	case float64:
		return aisvc.NormalizeEffort(strconv.FormatFloat(v, 'f', -1, 64))
	case int:
		return aisvc.NormalizeEffort(strconv.Itoa(v))
	default:
		return aisvc.EffortMedium
	}
}
