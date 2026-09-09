package apitoken

import (
	"context"
	"strings"

	"unified-proxy-pool/internal/auth"
)

func init() {
	// One implementation of the scope rule, evaluated by the auth middleware.
	auth.HasScope = HasScope
}

// Scope names used by the panel. A token is created with a set of these; the
// middleware checks the one a route needs.
const (
	ScopeAdmin         = "admin"
	ScopeProxiesRead   = "proxies:read"
	ScopeProxiesWrite  = "proxies:write"
	ScopeChannelsWrite = "channels:write"
	ScopeAIWrite       = "ai:write"
)

// DefaultScopes is what a token gets when the caller does not choose. Read-only
// is the safe default: a token minted for a monitoring script should not be
// able to push proxies or drive the AI endpoint.
const DefaultScopes = ScopeProxiesRead

// HasScope reports whether a token's scope string grants want.
//
// "admin" and "*" grant everything; "proxies:*" grants every proxies scope; and
// a write scope implies the matching read scope. Scopes were previously stored
// and displayed but never checked, so a token the UI labelled "proxies:read"
// could submit proxies and call the AI endpoint.
func HasScope(scopes, want string) bool {
	want = strings.TrimSpace(strings.ToLower(want))
	if want == "" {
		return true
	}
	wantGroup, _, _ := strings.Cut(want, ":")
	for _, raw := range strings.FieldsFunc(scopes, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';'
	}) {
		got := strings.TrimSpace(strings.ToLower(raw))
		switch got {
		case "", ScopeAdmin, "*":
			if got != "" {
				return true
			}
			continue
		}
		if got == want {
			return true
		}
		if group, action, ok := strings.Cut(got, ":"); ok {
			if group != wantGroup {
				continue
			}
			if action == "*" {
				return true
			}
			// write implies read
			if action == "write" && strings.HasSuffix(want, ":read") {
				return true
			}
		}
	}
	return false
}

// Grants reports whether this token may perform an action.
func (t Token) Grants(scope string) bool { return HasScope(t.Scopes, scope) }

// TokenScopes satisfies auth.TokenValidator: it reports the scopes of a valid
// token so the middleware can enforce them.
func (s *Store) TokenScopes(ctx context.Context, plain string) (string, bool) {
	t, ok := s.Validate(ctx, plain)
	if !ok {
		return "", false
	}
	return t.Scopes, true
}
