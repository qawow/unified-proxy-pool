package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type Entry struct {
	ID     int64          `json:"id"`
	At     time.Time      `json:"at"`
	Actor  string         `json:"actor"`
	Action string         `json:"action"`
	Detail map[string]any `json:"detail,omitempty"`
	IP     string         `json:"ip"`
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Log(ctx context.Context, actor, action, ip string, detail map[string]any) {
	if s == nil || s.db == nil {
		return
	}
	raw := "{}"
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			raw = string(b)
		}
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO audit_logs(at, actor, action, detail_json, ip) VALUES(?,?,?,?,?)`,
		time.Now().UTC(), actor, action, raw, ip)
}

func (s *Store) List(ctx context.Context, page, size int, action string) ([]Entry, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, nil
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	where := `WHERE 1=1`
	args := []any{}
	if action != "" {
		where += ` AND action = ?`
		args = append(args, action)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args2 := append(append([]any{}, args...), size, (page-1)*size)
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, actor, action, detail_json, ip FROM audit_logs `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, args2...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var raw string
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &raw, &e.IP); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(raw), &e.Detail)
		out = append(out, e)
	}
	return out, total, nil
}
