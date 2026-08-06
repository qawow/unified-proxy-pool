package apitoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type Token struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Scopes    string     `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
	// only on create
	Plain string `json:"token,omitempty"`
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func (s *Store) Create(ctx context.Context, name, scopes string) (Token, error) {
	if s == nil || s.db == nil {
		return Token{}, fmt.Errorf("token store unavailable")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "token"
	}
	if scopes == "" {
		scopes = "proxies:read"
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return Token{}, err
	}
	plain := "upp_" + hex.EncodeToString(b)
	prefix := plain[:12]
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens(name, token_hash, prefix, scopes, created_at) VALUES(?,?,?,?,?)`,
		name, hashToken(plain), prefix, scopes, now)
	if err != nil {
		return Token{}, err
	}
	id, _ := res.LastInsertId()
	return Token{ID: id, Name: name, Prefix: prefix, Scopes: scopes, CreatedAt: now, Plain: plain}, nil
}

func (s *Store) List(ctx context.Context) ([]Token, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, prefix, scopes, created_at, last_used_at FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		var last sql.NullTime
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.Scopes, &t.CreatedAt, &last); err != nil {
			continue
		}
		if last.Valid {
			tt := last.Time.UTC()
			t.LastUsed = &tt
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	return err
}

func (s *Store) Validate(ctx context.Context, plain string) (Token, bool) {
	if s == nil || s.db == nil || plain == "" {
		return Token{}, false
	}
	h := hashToken(plain)
	var t Token
	var last sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id, name, prefix, scopes, created_at, last_used_at FROM api_tokens WHERE token_hash = ?`, h).
		Scan(&t.ID, &t.Name, &t.Prefix, &t.Scopes, &t.CreatedAt, &last)
	if err != nil {
		return Token{}, false
	}
	if last.Valid {
		tt := last.Time.UTC()
		t.LastUsed = &tt
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, time.Now().UTC(), t.ID)
	return t, true
}

// ValidateToken satisfies auth.TokenValidator so the store can be passed
// directly to RequireAuthOrToken without an import cycle.
func (s *Store) ValidateToken(ctx context.Context, plain string) bool {
	_, ok := s.Validate(ctx, plain)
	return ok
}
