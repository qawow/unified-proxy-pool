package aisvc

import "sync"

// Built-in default prompts. Users can edit them via the panel or API; edited
// versions are persisted to SQLite (ai_prompts table).

type Prompt struct {
	Name        string `json:"name"`                 // key, e.g. "proxy_extract"
	Title       string `json:"title"`                // display label
	Description string `json:"description,omitempty"` // UI hint
	System      string `json:"system"`               // system-level instruction
	User        string `json:"user,omitempty"`       // user message template
	Default     bool   `json:"default"`              // true if unmodified built-in
	Builtin     bool   `json:"builtin"`              // shipped with binary
}

var DefaultPrompts = []Prompt{
	{
		Name:        "proxy_extract",
		Title:       "提取免费代理列表",
		Description: "从网页/搜索内容中提取免费 HTTP/SOCKS 代理地址",
		Builtin:     true,
		System: `You are a proxy list extractor. Read the user's content carefully.
Extract all free proxy addresses (HTTP/HTTPS/SOCKS4/SOCKS5) into a JSON array.
Each item must be a string "host:port" (or "protocol://host:port" when the protocol is known).
Return ONLY valid JSON, no markdown fences, no commentary. Example:
["1.2.3.4:8080","socks5://5.6.7.8:1080"]
If none found, return [].`,
		User: "Content to extract proxies from:\n\n{{.Content}}",
	},
	{
		Name:        "proxy_discover",
		Title:       "分析网页寻找代理线索",
		Description: "分析给定 URL 内容，找出潜在代理列表、链接或线索",
		Builtin:     true,
		System: `You are a proxy hunter. Analyze the provided web page content.
Find: (1) explicit proxy addresses, (2) links to other proxy list pages,
(3) any upstream/list sources worth crawling.
Respond with JSON: {"proxies":["host:port",...],"links":["url",...],"sources":["..."]}
Return ONLY valid JSON, no markdown. Use [] for empty fields.`,
		User: "Web page content:\n\n{{.Content}}",
	},
	{
		Name:        "proxy_ai_generate",
		Title:       "AI 生成候选代理",
		Description: "让 AI 根据常识/已知资源生成常见免费代理候选（不保证可用）",
		Builtin:     true,
		System: `You are a proxy knowledge base. Based on your knowledge of commonly
published public proxy hosts and ports (HTTP :80/8080/3128, SOCKS5 :1080,
etc.), generate a list of plausible candidate proxies.
These are candidates only; they may or may not be alive.
Return ONLY a JSON array of "host:port" strings, no markdown.`,
		User: "Generate up to {{.Count}} candidate proxies. {{.Content}}",
	},
}

// PromptStore persists edited prompts. Zero value keeps prompts in memory only.
type PromptStore struct {
	// DB is optional; when nil, edits live in memory.
	DB      interface {
		Exec(query string, args ...any) (result interface {
			RowsAffected() (int64, error)
		}, err error)
	}
	mu      sync.Mutex
	current []Prompt
}

func NewPromptStore() *PromptStore {
	current := clonePrompts(DefaultPrompts)
	for i := range current {
		if current[i].Builtin {
			current[i].Default = true
		}
	}
	return &PromptStore{current: current}
}

func clonePrompts(in []Prompt) []Prompt {
	out := make([]Prompt, len(in))
	copy(out, in)
	return out
}

func (s *PromptStore) List() []Prompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clonePrompts(s.current)
}

func (s *PromptStore) Get(name string) (Prompt, bool) {
	for _, p := range s.List() {
		if p.Name == name {
			return p, true
		}
	}
	return Prompt{}, false
}

func (s *PromptStore) Upsert(p Prompt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.current {
		if s.current[i].Name == p.Name {
			p.Default = p.System == DefaultSystem(p.Name) && p.User == DefaultUser(p.Name)
			p.Builtin = s.current[i].Builtin
			s.current[i] = p
			return
		}
	}
	p.Default = false
	s.current = append(s.current, p)
}

func (s *PromptStore) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.current {
		if s.current[i].Name == name {
			if s.current[i].Builtin {
				// reset built-in instead of removing
				d := defaultByName(name)
				d.Default = true
				s.current[i] = d
			} else {
				s.current = append(s.current[:i], s.current[i+1:]...)
			}
			return true
		}
	}
	return false
}

func defaultByName(name string) Prompt {
	for _, p := range DefaultPrompts {
		if p.Name == name {
			return p
		}
	}
	return Prompt{}
}

func DefaultSystem(name string) string { return defaultByName(name).System }
func DefaultUser(name string) string   { return defaultByName(name).User }
