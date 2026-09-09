package apitoken

import "testing"

func TestHasScope(t *testing.T) {
	cases := []struct {
		scopes string
		want   string
		ok     bool
	}{
		// The UI mints this by default; it must not authorise writes.
		{"proxies:read", ScopeProxiesWrite, false},
		{"proxies:read", ScopeAIWrite, false},
		{"proxies:read", ScopeProxiesRead, true},
		{"proxies:write", ScopeProxiesWrite, true},
		// write implies read
		{"proxies:write", ScopeProxiesRead, true},
		{"proxies:*", ScopeProxiesWrite, true},
		{"proxies:*", ScopeAIWrite, false},
		{"admin", ScopeAIWrite, true},
		{"*", ScopeChannelsWrite, true},
		{"proxies:read, ai:write", ScopeAIWrite, true},
		{"", ScopeProxiesRead, false},
	}
	for _, c := range cases {
		if got := HasScope(c.scopes, c.want); got != c.ok {
			t.Errorf("HasScope(%q, %q) = %v, want %v", c.scopes, c.want, got, c.ok)
		}
	}
}
