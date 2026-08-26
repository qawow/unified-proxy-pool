package chanpolicy

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"
)

// Persister stores active bans so a restart does not immediately release every
// sidelined proxy. Counters are deliberately not persisted: they are a short
// sliding window, and reloading a stale window would ban on old evidence.
//
// Implementations must be safe for concurrent use and must not block for long —
// they are called from Record, off the lock but still inline.
type Persister interface {
	SaveBan(b Ban)
	DeleteBan(channel, addr string)
	DeleteChannel(channel string)
	LoadBans() ([]Ban, error)
	SaveOutcome(e LogEntry)
	LoadOutcomes(limit int) ([]LogEntry, error)
	PruneOutcomes(before time.Time)
	SaveAllow(channel, addr, reason string)
	DeleteAllow(channel, addr string)
	LoadAllows() ([]Allow, error)
	SaveRule(r Rule)
	DeleteRule(id string)
	LoadRules() ([]Rule, error)
}

// SQLStore persists bans in the app's SQLite database.
type SQLStore struct {
	db *sql.DB
	// now must agree with the Registry's clock: the store decides which rows are
	// expired, and if the two disagree it prunes bans the registry still considers
	// live (or keeps ones it has released).
	now func() time.Time
}

func NewSQLStore(db *sql.DB) *SQLStore {
	return &SQLStore{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the store's clock. Tests use it to stay in step with the
// Registry's injected clock.
func (s *SQLStore) SetClock(now func() time.Time) {
	if s != nil && now != nil {
		s.now = now
	}
}

func (s *SQLStore) clock() time.Time {
	if s == nil || s.now == nil {
		return time.Now().UTC()
	}
	return s.now()
}

func (s *SQLStore) SaveBan(b Ban) {
	if s == nil || s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_bans (channel, addr, reason, banned_at, until_at, strikes)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel, addr) DO UPDATE SET
			reason = excluded.reason,
			banned_at = excluded.banned_at,
			until_at = excluded.until_at,
			strikes = excluded.strikes`,
		b.Channel, b.Addr, b.Reason, b.BannedAt.UTC(), b.Until.UTC(), b.Strikes)
	if err != nil {
		log.Printf("chanpolicy: save ban %s/%s: %v", b.Channel, b.Addr, err)
	}
}

func (s *SQLStore) DeleteBan(channel, addr string) {
	if s == nil || s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM channel_bans WHERE channel = ? AND addr = ?`, channel, addr); err != nil {
		log.Printf("chanpolicy: delete ban %s/%s: %v", channel, addr, err)
	}
}

func (s *SQLStore) DeleteChannel(channel string) {
	if s == nil || s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM channel_bans WHERE channel = ?`, channel); err != nil {
		log.Printf("chanpolicy: delete channel %s: %v", channel, err)
	}
}

// LoadBans returns the bans that have not expired yet, clearing out the ones that
// have.
func (s *SQLStore) LoadBans() ([]Ban, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := s.clock()
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM channel_bans WHERE until_at <= ?`, now); err != nil {
		log.Printf("chanpolicy: prune expired bans: %v", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT channel, addr, reason, banned_at, until_at, strikes
		FROM channel_bans WHERE until_at > ?`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ban
	for rows.Next() {
		var b Ban
		if err := rows.Scan(&b.Channel, &b.Addr, &b.Reason, &b.BannedAt, &b.Until, &b.Strikes); err != nil {
			return nil, err
		}
		b.TTLSec = int(b.Until.Sub(b.BannedAt) / time.Second)
		out = append(out, b)
	}
	return out, rows.Err()
}

// Restore reinstates persisted bans. Expired ones are skipped, so a long
// downtime releases everything naturally.
func (r *Registry) Restore(bans []Ban) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	n := 0
	for _, b := range bans {
		if b.Until.IsZero() || !b.Until.After(now) {
			continue
		}
		channel := NormalizeChannelName(b.Channel)
		if channel == "" {
			continue
		}
		ch := r.channelLocked(channel, now)
		e := r.entryLocked(ch, b.Addr, now)
		e.bannedUntil = b.Until
		e.banReason = b.Reason
		e.strikes = b.Strikes
		e.lastBanAt = b.BannedAt
		if b.Until.After(ch.lastBanAt) {
			ch.lastBanAt = b.BannedAt
		}
		n++
	}
	return n
}

// StartSweeper runs Sweep periodically until ctx is done.
func (r *Registry) StartSweeper(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.Sweep()
				if r.persist != nil {
					r.persist.PruneOutcomes(r.now().Add(-r.logRetain()))
				}
			}
		}
	}()
}

func (r *Registry) logRetain() time.Duration {
	r.mu.RLock()
	hours := r.policy.LogRetainHours
	r.mu.RUnlock()
	if hours <= 0 {
		hours = 48
	}
	return time.Duration(hours) * time.Hour
}

func (s *SQLStore) SaveOutcome(e LogEntry) {
	if s == nil || s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ok, reported, banned := 0, 0, 0
	if e.OK {
		ok = 1
	}
	if e.Reported {
		reported = 1
	}
	if e.Banned {
		banned = 1
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_outcomes(at, channel, addr, ok, status, err, latency_ms, reported, banned, reason)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		e.At.UTC(), e.Channel, e.Addr, ok, e.Status, e.Err, e.LatencyMS, reported, banned, e.Reason); err != nil {
		log.Printf("chanpolicy: save outcome: %v", err)
	}
}

func (s *SQLStore) LoadOutcomes(limit int) ([]LogEntry, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultLogCap
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT at, channel, addr, ok, status, err, latency_ms, reported, banned, reason
		FROM channel_outcomes ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rev []LogEntry
	for rows.Next() {
		var e LogEntry
		var ok, reported, banned int
		if err := rows.Scan(&e.At, &e.Channel, &e.Addr, &ok, &e.Status, &e.Err, &e.LatencyMS, &reported, &banned, &e.Reason); err != nil {
			return nil, err
		}
		e.OK = ok != 0
		e.Reported = reported != 0
		e.Banned = banned != 0
		rev = append(rev, e)
	}
	// Query is newest-first; the ring is newest-last.
	out := make([]LogEntry, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out, rows.Err()
}

func (s *SQLStore) PruneOutcomes(before time.Time) {
	if s == nil || s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM channel_outcomes WHERE at < ?`, before.UTC()); err != nil {
		log.Printf("chanpolicy: prune outcomes: %v", err)
	}
}

// Allow is a (channel, addr) pair automatic rules must not ban.
// Empty Channel means the addr is protected on every channel.
type Allow struct {
	Channel   string    `json:"channel"`
	Addr      string    `json:"addr"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *SQLStore) SaveAllow(channel, addr, reason string) {
	if s == nil || s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_allowlist(channel, addr, reason, created_at)
		VALUES(?,?,?,?)
		ON CONFLICT(channel, addr) DO UPDATE SET reason = excluded.reason`,
		channel, addr, reason, time.Now().UTC()); err != nil {
		log.Printf("chanpolicy: save allow %s/%s: %v", channel, addr, err)
	}
}

func (s *SQLStore) DeleteAllow(channel, addr string) {
	if s == nil || s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM channel_allowlist WHERE channel = ? AND addr = ?`, channel, addr); err != nil {
		log.Printf("chanpolicy: delete allow %s/%s: %v", channel, addr, err)
	}
}

func (s *SQLStore) LoadAllows() ([]Allow, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT channel, addr, reason, created_at FROM channel_allowlist`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Allow
	for rows.Next() {
		var a Allow
		if err := rows.Scan(&a.Channel, &a.Addr, &a.Reason, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *SQLStore) SaveRule(r Rule) {
	if s == nil || s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	statuses := ""
	if len(r.Statuses) > 0 {
		parts := make([]string, len(r.Statuses))
		for i, n := range r.Statuses {
			parts[i] = itoa(n)
		}
		statuses = strings.Join(parts, ",")
	}
	enabled := 0
	if r.Enabled {
		enabled = 1
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_rules(id, name, channel, kind, statuses, threshold, rate, min_samples, match, ttl_sec, enabled, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, channel=excluded.channel, kind=excluded.kind,
			statuses=excluded.statuses, threshold=excluded.threshold, rate=excluded.rate,
			min_samples=excluded.min_samples, match=excluded.match, ttl_sec=excluded.ttl_sec,
			enabled=excluded.enabled`,
		r.ID, r.Name, r.Channel, r.Kind, statuses, r.Threshold, r.Rate, r.MinSamples, r.Match, r.TTLSec, enabled, r.CreatedAt.UTC()); err != nil {
		log.Printf("chanpolicy: save rule %s: %v", r.ID, err)
	}
}

func (s *SQLStore) DeleteRule(id string) {
	if s == nil || s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM channel_rules WHERE id = ?`, id); err != nil {
		log.Printf("chanpolicy: delete rule %s: %v", id, err)
	}
}

func (s *SQLStore) LoadRules() ([]Rule, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, channel, kind, statuses, threshold, rate, min_samples, match, ttl_sec, enabled, created_at FROM channel_rules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		var statuses string
		var enabled int
		if err := rows.Scan(&r.ID, &r.Name, &r.Channel, &r.Kind, &statuses, &r.Threshold, &r.Rate, &r.MinSamples, &r.Match, &r.TTLSec, &enabled, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		if statuses != "" {
			for _, part := range strings.Split(statuses, ",") {
				n := 0
				ok := true
				for _, c := range strings.TrimSpace(part) {
					if c < '0' || c > '9' {
						ok = false
						break
					}
					n = n*10 + int(c-'0')
				}
				if ok && n > 0 {
					r.Statuses = append(r.Statuses, n)
				}
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
