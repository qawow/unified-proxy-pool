package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/settings"
)

const SessionCookieName = "spp_session"

type session struct {
	ExpiresAt time.Time
}

type Service struct {
	settings      *settings.Service
	store         *db.Store
	sessionMaxAge time.Duration
	mu            sync.RWMutex
	sessions      map[string]session
}

func (s *Service) SetSessionMaxAgeSec(sec int) {
	if sec <= 0 {
		sec = 7 * 24 * 3600
	}
	s.sessionMaxAge = time.Duration(sec) * time.Second
}

func NewService(settingsSvc *settings.Service, store *db.Store, sessionMaxAgeSec int) *Service {
	if sessionMaxAgeSec <= 0 {
		sessionMaxAgeSec = 7 * 24 * 3600
	}
	s := &Service{
		settings:      settingsSvc,
		store:         store,
		sessionMaxAge: time.Duration(sessionMaxAgeSec) * time.Second,
		sessions:      make(map[string]session),
	}
	s.loadFromDB()
	go s.cleanupLoop()
	return s
}

func (s *Service) loadFromDB() {
	if s.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.store.DB.QueryContext(ctx, `SELECT token, expires_at FROM auth_sessions WHERE expires_at > ?`, time.Now().UTC())
	if err != nil {
		return
	}
	defer rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var token string
		var exp time.Time
		if err := rows.Scan(&token, &exp); err != nil {
			continue
		}
		s.sessions[token] = session{ExpiresAt: exp}
	}
}

func (s *Service) cleanupLoop() {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.purgeExpired()
	}
}

func (s *Service) purgeExpired() {
	now := time.Now().UTC()
	s.mu.Lock()
	for token, sess := range s.sessions {
		if !sess.ExpiresAt.After(now) {
			delete(s.sessions, token)
		}
	}
	s.mu.Unlock()
	if s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.store.DB.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at <= ?`, now)
	}
}

// MinPasswordLen is enforced server-side: the panel's own form controls are
// not a constraint (a misclick could previously set an empty password, and this
// login guards a proxy that can reach the whole LAN).
const MinPasswordLen = 6

func ValidatePassword(password string) error {
	if len(strings.TrimSpace(password)) < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	return nil
}

func HashPassword(password string) (string, error) {
	data, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(data), err
}

func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func (s *Service) Login(ctx context.Context, password string) (string, error) {
	current, err := s.settings.Get(ctx)
	if err != nil {
		return "", err
	}
	if !VerifyPassword(current.PasswordHash, password) {
		return "", errors.New("password incorrect")
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	exp := time.Now().UTC().Add(s.sessionMaxAge)
	s.mu.Lock()
	s.sessions[token] = session{ExpiresAt: exp}
	s.mu.Unlock()
	if s.store != nil {
		_, _ = s.store.DB.ExecContext(ctx, `INSERT OR REPLACE INTO auth_sessions(token, expires_at, created_at) VALUES(?,?,?)`,
			token, exp, time.Now().UTC())
	}
	return token, nil
}

func (s *Service) Logout(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
	if s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.store.DB.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token = ?`, token)
	}
}

func (s *Service) ChangePassword(ctx context.Context, oldPassword, newPassword string) error {
	current, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	if !VerifyPassword(current.PasswordHash, oldPassword) {
		return errors.New("old password incorrect")
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.settings.UpdatePasswordHash(ctx, hash); err != nil {
		return err
	}
	s.InvalidateAllSessions()
	return nil
}

func (s *Service) InvalidateAllSessions() {
	s.mu.Lock()
	s.sessions = make(map[string]session)
	s.mu.Unlock()
	if s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.store.DB.ExecContext(ctx, `DELETE FROM auth_sessions`)
	}
}

func (s *Service) IsAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return false
	}
	now := time.Now().UTC()
	s.mu.RLock()
	current, ok := s.sessions[cookie.Value]
	s.mu.RUnlock()
	if ok && current.ExpiresAt.After(now) {
		return true
	}
	// fallback DB (in case memory missed after concurrent load)
	if s.store != nil && !ok {
		var exp time.Time
		err := s.store.DB.QueryRowContext(r.Context(), `SELECT expires_at FROM auth_sessions WHERE token = ?`, cookie.Value).Scan(&exp)
		if err == nil && exp.After(now) {
			s.mu.Lock()
			s.sessions[cookie.Value] = session{ExpiresAt: exp}
			s.mu.Unlock()
			return true
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			_ = err
		}
	}
	if ok {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
		if s.store != nil {
			_, _ = s.store.DB.ExecContext(context.Background(), `DELETE FROM auth_sessions WHERE token = ?`, cookie.Value)
		}
	}
	return false
}

func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.IsAuthenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			writeUnauthorizedJSON(w)
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

// RequireAuthOrToken accepts a valid session cookie OR a Bearer API token.
// It is used on endpoints that scripts call — scripts cannot maintain a session
// cookie, so token auth is the only viable alternative to open access.
//
// A session cookie is an admin login and passes unconditionally. A token must
// carry the scope the route requires: scopes used to be recorded and shown in
// the UI but never enforced, so a token labelled "proxies:read" could push
// proxies into the pool and drive the outbound AI call.
func (s *Service) RequireAuthOrToken(tokenValidator TokenValidator, scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.IsAuthenticated(r) {
				next.ServeHTTP(w, r)
				return
			}
			// Try Bearer token: Authorization: Bearer upp_…
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				plain := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
				if tokenValidator != nil {
					if scopes, ok := tokenValidator.TokenScopes(r.Context(), plain); ok {
						if scope == "" || HasScope(scopes, scope) {
							next.ServeHTTP(w, r.WithContext(withTokenScopes(r.Context(), scopes)))
							return
						}
						writeForbiddenJSON(w, scope)
						return
					}
				}
			}
			writeUnauthorizedJSON(w)
		})
	}
}

// TokenValidator is the minimum interface the auth middleware needs from
// apitoken.Store. Keeping it here avoids an import cycle.
// The concrete implementation is *apitoken.Store.
type TokenValidator interface {
	ValidateToken(ctx context.Context, plain string) bool
	// TokenScopes returns the token's scope string when the token is valid.
	TokenScopes(ctx context.Context, plain string) (string, bool)
}

// HasScope is set by the apitoken package at init so the auth middleware can
// evaluate scopes without importing it (apitoken imports nothing from auth,
// but keeping the rule in one place avoids two divergent implementations).
var HasScope = func(scopes, want string) bool { return true }

type tokenScopeKey struct{}

func withTokenScopes(ctx context.Context, scopes string) context.Context {
	return context.WithValue(ctx, tokenScopeKey{}, scopes)
}

// TokenScopesFrom returns the scopes of the API token that authorised this
// request, or "" for a session login.
func TokenScopesFrom(ctx context.Context) string {
	s, _ := ctx.Value(tokenScopeKey{}).(string)
	return s
}

func (s *Service) SetSessionCookie(w http.ResponseWriter, token string) {
	maxAge := int(s.sessionMaxAge.Seconds())
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		Expires:  time.Now().Add(s.sessionMaxAge),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func writeForbiddenJSON(w http.ResponseWriter, scope string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"success":false,"message":"token is missing the ` + scope + ` scope"}`))
}

func writeUnauthorizedJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"success":false,"message":"unauthorized"}`))
}
