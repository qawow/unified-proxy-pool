package blacklist

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Host      string     `json:"host"`
	Reason    string     `json:"reason"`
	Until     *time.Time `json:"until,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type Store struct {
	db *sql.DB
	mu sync.RWMutex
	// host -> until (zero = permanent)
	mem map[string]time.Time
}

func New(db *sql.DB) *Store {
	s := &Store{db: db, mem: map[string]time.Time{}}
	s.reload()
	return s
}

func (s *Store) reload() {
	if s.db == nil {
		return
	}
	rows, err := s.db.Query(`SELECT host, reason, until_at, created_at FROM proxy_blacklist`)
	if err != nil {
		return
	}
	defer rows.Close()
	mem := map[string]time.Time{}
	now := time.Now().UTC()
	for rows.Next() {
		var host, reason string
		var until sql.NullTime
		var created time.Time
		if err := rows.Scan(&host, &reason, &until, &created); err != nil {
			continue
		}
		host = normalizeHost(host)
		if host == "" {
			continue
		}
		if until.Valid && until.Time.Before(now) {
			continue
		}
		if until.Valid {
			mem[host] = until.Time.UTC()
		} else {
			mem[host] = time.Time{}
		}
	}
	s.mu.Lock()
	s.mem = mem
	s.mu.Unlock()
}

func normalizeHost(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if i := strings.Index(v, "://"); i >= 0 {
		v = v[i+3:]
	}
	if i := strings.Index(v, "/"); i >= 0 {
		v = v[:i]
	}
	// keep host:port as key when full addr given
	return v
}

func hostKey(addrOrHost string) string {
	h := normalizeHost(addrOrHost)
	if i := strings.LastIndex(h, ":"); i > 0 {
		// if looks like host:port keep full; also index bare host
		return h
	}
	return h
}

func bareHost(addrOrHost string) string {
	h := hostKey(addrOrHost)
	if i := strings.LastIndex(h, ":"); i > 0 {
		return h[:i]
	}
	return h
}

func (s *Store) Blocked(addrOrHost string) bool {
	if s == nil {
		return false
	}
	full := hostKey(addrOrHost)
	bare := bareHost(addrOrHost)
	now := time.Now().UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range []string{full, bare} {
		until, ok := s.mem[key]
		if !ok {
			continue
		}
		if until.IsZero() || until.After(now) {
			return true
		}
	}
	return false
}

func (s *Store) Add(ctx context.Context, host, reason string, ttlSec int) (Entry, error) {
	host = hostKey(host)
	if host == "" {
		return Entry{}, errInvalid
	}
	now := time.Now().UTC()
	var until *time.Time
	var untilVal interface{}
	if ttlSec > 0 {
		t := now.Add(time.Duration(ttlSec) * time.Second)
		until = &t
		untilVal = t
	}
	if s.db != nil {
		_, err := s.db.ExecContext(ctx, `INSERT INTO proxy_blacklist(host, reason, until_at, created_at) VALUES(?,?,?,?)
			ON CONFLICT(host) DO UPDATE SET reason=excluded.reason, until_at=excluded.until_at`,
			host, reason, untilVal, now)
		if err != nil {
			return Entry{}, err
		}
	}
	s.mu.Lock()
	if until != nil {
		s.mem[host] = *until
	} else {
		s.mem[host] = time.Time{}
	}
	s.mu.Unlock()
	return Entry{Host: host, Reason: reason, Until: until, CreatedAt: now}, nil
}

func (s *Store) Remove(ctx context.Context, host string) error {
	host = hostKey(host)
	if s.db != nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM proxy_blacklist WHERE host = ? OR host LIKE ?`, host, bareHost(host)+":%")
	}
	s.mu.Lock()
	delete(s.mem, host)
	delete(s.mem, bareHost(host))
	s.mu.Unlock()
	return nil
}

func (s *Store) List(ctx context.Context) ([]Entry, error) {
	s.reload()
	if s.db == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		out := make([]Entry, 0, len(s.mem))
		for h, u := range s.mem {
			e := Entry{Host: h, CreatedAt: time.Now().UTC()}
			if !u.IsZero() {
				t := u
				e.Until = &t
			}
			out = append(out, e)
		}
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT host, reason, until_at, created_at FROM proxy_blacklist ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	now := time.Now().UTC()
	for rows.Next() {
		var e Entry
		var until sql.NullTime
		if err := rows.Scan(&e.Host, &e.Reason, &until, &e.CreatedAt); err != nil {
			continue
		}
		if until.Valid {
			if until.Time.Before(now) {
				continue
			}
			t := until.Time.UTC()
			e.Until = &t
		}
		out = append(out, e)
	}
	return out, nil
}

var errInvalid = errString("invalid host")

type errString string

func (e errString) Error() string { return string(e) }
