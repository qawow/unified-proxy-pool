package chanpolicy

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Outcome is one observed request result for a (channel, proxy) pair.
type Outcome struct {
	Channel   string
	Addr      string
	OK        bool
	Status    int    // HTTP status; 0 when unknowable (HTTPS CONNECT tunnel)
	Err       string // short reason tag: dial_failed, timeout, reported, ...
	LatencyMS int64
	Reported  bool // true when it came from a caller, false when observed locally
}

// Ban is an active or expired temporary ban.
type Ban struct {
	Channel  string    `json:"channel"`
	Addr     string    `json:"addr"`
	Reason   string    `json:"reason"`
	BannedAt time.Time `json:"banned_at"`
	Until    time.Time `json:"until"`
	Strikes  int       `json:"strikes"`
	TTLSec   int       `json:"ttl_sec"`
	Pending  bool      `json:"pending,omitempty"` // TTL elapsed, waiting for a success
}

// ChannelStat is the per-channel summary the panel lists.
type ChannelStat struct {
	Name          string    `json:"name"`
	OK            int       `json:"ok"`
	Fail          int       `json:"fail"`
	Timeout       int       `json:"timeout"`
	FailRate      float64   `json:"fail_rate"`
	Bans          int       `json:"bans"`
	Entries       int       `json:"entries"`
	LastOutcomeAt time.Time `json:"last_outcome_at,omitempty"`
	LastBanAt     time.Time `json:"last_ban_at,omitempty"`
}

type entry struct {
	counters
	bannedUntil    time.Time
	banReason      string
	strikes        int
	lastBanAt      time.Time
	lastSeenAt     time.Time
	pendingReprobe bool
}

func (e *entry) banned(now time.Time) bool {
	if !e.bannedUntil.IsZero() && e.bannedUntil.After(now) {
		return true
	}
	// TTL elapsed, but we have not seen a success since. Stay out of rotation
	// until something actually works — otherwise a just-banned IP is handed
	// back the moment the clock ticks over.
	return e.pendingReprobe
}

type channelState struct {
	name       string
	entries    map[string]*entry
	lastSeenAt time.Time
	lastBanAt  time.Time
}

// Registry is the whole per-channel policy state. Safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	chans  map[string]*channelState
	policy Policy

	// now is injectable so tests can advance time without sleeping.
	now func() time.Time

	onBan   func(Ban)
	onUnban func(Ban)
	persist Persister
	log     *logRing
	allows  map[string]map[string]Allow // channel -> addr -> meta; "" = global
	rules   []Rule
}

// Options configures a new Registry.
type Options struct {
	Policy  Policy
	Now     func() time.Time
	OnBan   func(Ban)
	OnUnban func(Ban)
	Persist Persister
}

func New(opts Options) *Registry {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Registry{
		chans:   map[string]*channelState{},
		policy:  opts.Policy.Normalize(),
		now:     now,
		onBan:   opts.OnBan,
		onUnban: opts.OnUnban,
		persist: opts.Persist,
		log:     newLogRing(defaultLogCap),
		allows:  map[string]map[string]Allow{},
		rules:   nil,
	}
}

// Policy returns the active policy.
func (r *Registry) Policy() Policy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.policy
}

// SetPolicy hot-applies a new policy.
//
// Changing the window length reinterprets every bucket, so counters are dropped
// when it changes. Bans are kept: they are decisions already made, and releasing
// every banned proxy because someone edited a threshold is worse than carrying a
// few stale bans to their natural expiry.
func (r *Registry) SetPolicy(p Policy) {
	p = p.Normalize()
	r.mu.Lock()
	defer r.mu.Unlock()
	windowChanged := r.policy.WindowSec != p.WindowSec
	r.policy = p
	if windowChanged {
		for _, ch := range r.chans {
			for _, e := range ch.entries {
				e.counters = counters{}
			}
		}
	}
}

// Enabled reports whether banning and channel scoping are active.
func (r *Registry) Enabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.policy.Enabled
}

// ChannelFor derives the channel name for a target under the active key mode.
func (r *Registry) ChannelFor(target string) string {
	r.mu.RLock()
	mode := r.policy.KeyMode
	enabled := r.policy.Enabled
	r.mu.RUnlock()
	if !enabled {
		return ""
	}
	return Normalize(target, mode)
}

func (r *Registry) bucketSec() int64 {
	sec := int64(r.policy.WindowSec) / numBuckets
	if sec <= 0 {
		sec = 1
	}
	return sec
}

// Record files one outcome and evaluates the ban rules for that pair.
//
// It returns the ban that was just created, or nil. Hooks fire outside the lock
// so a slow webhook cannot stall the selection path.
func (r *Registry) Record(o Outcome) *Ban {
	channel := NormalizeChannelName(o.Channel)
	addr := strings.ToLower(strings.TrimSpace(o.Addr))
	if channel == "" {
		channel = DefaultChannel
	}
	if addr == "" {
		return nil
	}

	r.mu.Lock()
	if !r.policy.Enabled {
		r.mu.Unlock()
		return nil
	}
	now := r.now()
	ch := r.channelLocked(channel, now)
	e := r.entryLocked(ch, addr, now)

	ch.lastSeenAt = now
	e.lastSeenAt = now
	e.record(now, r.bucketSec(), o.OK, isTimeout(o), r.policy.window())
	if o.OK && e.pendingReprobe {
		e.pendingReprobe = false
		e.bannedUntil = time.Time{}
		e.banReason = ""
	}

	ban := r.evaluateLocked(ch, addr, e, o, now)
	r.mu.Unlock()

	// The log lives outside the registry lock so a slow panel poll cannot stall
	// Record. Banned is set only when *this* outcome fired the rule, so the
	// panel can highlight the triggering line rather than every subsequent miss.
	if r.log != nil {
		entry := LogEntry{
			At: now, Channel: channel, Addr: addr,
			OK: o.OK, Status: o.Status, Err: o.Err,
			LatencyMS: o.LatencyMS, Reported: o.Reported,
		}
		if ban != nil {
			entry.Banned = true
			entry.Reason = ban.Reason
		}
		r.log.add(entry)
		if r.persist != nil {
			r.persist.SaveOutcome(entry)
		}
	}

	if ban != nil {
		if r.persist != nil {
			r.persist.SaveBan(*ban)
		}
		if r.onBan != nil {
			r.onBan(*ban)
		}
	}
	return ban
}

// isTimeout classifies an outcome as a timeout so timeouts can trip their own
// rule: a site that times out is usually throttling, while a refused connection
// is usually a dead proxy, and the two deserve different thresholds.
func isTimeout(o Outcome) bool {
	if o.OK {
		return false
	}
	e := strings.ToLower(o.Err)
	return strings.Contains(e, "timeout") || strings.Contains(e, "deadline") ||
		o.Status == 408 || o.Status == 504
}

// evaluateLocked applies the rules in order of certainty: an explicit status code
// is a stronger signal than a rate, so it is checked first and its reason wins.
func (r *Registry) evaluateLocked(ch *channelState, addr string, e *entry, o Outcome, now time.Time) *Ban {
	if r.allowedLocked(ch.name, addr) {
		return nil
	}
	if e.banned(now) {
		return nil // already out; let it expire rather than stacking strikes
	}
	p := r.policy
	reason := ""
	switch {
	case p.bansStatus(o.Status):
		reason = "status_" + itoa(o.Status)
	case p.ConsecutiveFails > 0 && e.consecutive(now, p.window()) >= p.ConsecutiveFails:
		reason = "consecutive_fails"
	default:
		ok, fail, timeout := e.sum(now, r.bucketSec())
		switch {
		case p.TimeoutFails > 0 && timeout >= p.TimeoutFails:
			reason = "timeouts"
		case p.FailRate > 0 && ok+fail >= p.MinSamples &&
			float64(fail)/float64(ok+fail) >= p.FailRate:
			reason = "fail_rate"
		}
	}
	if reason == "" {
			for _, rule := range r.rules {
				if !rule.appliesTo(ch.name) {
					continue
				}
				if why, hit := rule.matches(o, e, now, p.window(), r.bucketSec()); hit {
					reason = why
					break
				}
			}
		}
		if reason == "" {
			return nil
		}
		if o.Reported {
			reason += "_reported"
		}

	// Reset the escalation ladder when the pair has behaved for a while, so an
	// occasional offender does not creep up to the maximum ban and stay there.
	if !e.lastBanAt.IsZero() && now.Sub(e.lastBanAt) > p.idleResetAfter() {
		e.strikes = 0
	}
	ttl := banTTL(p, e.strikes)
	e.strikes++
	e.bannedUntil = now.Add(ttl)
	e.banReason = reason
	e.lastBanAt = now
	e.pendingReprobe = p.ReprobeOnExpiry
	ch.lastBanAt = now

	return &Ban{
		Channel:  ch.name,
		Addr:     addr,
		Reason:   reason,
		BannedAt: now,
		Until:    e.bannedUntil,
		Strikes:  e.strikes,
		TTLSec:   int(ttl / time.Second),
	}
}

// banTTL doubles the base TTL per prior strike, capped. Repeat offenders are
// sidelined for longer without ever being banned permanently.
func banTTL(p Policy, strikes int) time.Duration {
	ttl := time.Duration(p.BanTTLSec) * time.Second
	maxTTL := time.Duration(p.BanTTLMaxSec) * time.Second
	for i := 0; i < strikes && ttl < maxTTL; i++ {
		ttl *= 2
	}
	if ttl > maxTTL {
		ttl = maxTTL
	}
	return ttl
}

// channelLocked fetches or creates a channel, evicting the least recently used
// one when over the cap.
func (r *Registry) channelLocked(name string, now time.Time) *channelState {
	if ch, ok := r.chans[name]; ok {
		return ch
	}
	if len(r.chans) >= r.policy.MaxChannels {
		r.evictChannelLocked(now)
	}
	ch := &channelState{name: name, entries: map[string]*entry{}, lastSeenAt: now}
	r.chans[name] = ch
	return ch
}

// evictChannelLocked drops the stalest channel, preferring one with no live bans.
// Evicting a channel with active bans would silently un-ban proxies, so those are
// only sacrificed when every channel is holding bans.
func (r *Registry) evictChannelLocked(now time.Time) {
	var victim string
	var victimAt time.Time
	var fallback string
	var fallbackAt time.Time
	for name, ch := range r.chans {
		if ch.activeBans(now) > 0 {
			if fallback == "" || ch.lastSeenAt.Before(fallbackAt) {
				fallback, fallbackAt = name, ch.lastSeenAt
			}
			continue
		}
		if victim == "" || ch.lastSeenAt.Before(victimAt) {
			victim, victimAt = name, ch.lastSeenAt
		}
	}
	if victim == "" {
		victim = fallback
	}
	if victim != "" {
		if r.persist != nil {
			r.persist.DeleteChannel(victim)
		}
		delete(r.chans, victim)
	}
}

// entryLocked fetches or creates a pair entry, evicting within the channel when
// over the per-channel cap.
func (r *Registry) entryLocked(ch *channelState, addr string, now time.Time) *entry {
	if e, ok := ch.entries[addr]; ok {
		return e
	}
	if len(ch.entries) >= r.policy.MaxEntriesPerChan {
		r.evictEntryLocked(ch, now)
	}
	e := &entry{lastSeenAt: now}
	ch.entries[addr] = e
	return e
}

func (r *Registry) evictEntryLocked(ch *channelState, now time.Time) {
	var victim string
	var victimAt time.Time
	var fallback string
	var fallbackAt time.Time
	for addr, e := range ch.entries {
		if e.banned(now) {
			if fallback == "" || e.lastSeenAt.Before(fallbackAt) {
				fallback, fallbackAt = addr, e.lastSeenAt
			}
			continue
		}
		if victim == "" || e.lastSeenAt.Before(victimAt) {
			victim, victimAt = addr, e.lastSeenAt
		}
	}
	if victim == "" {
		victim = fallback
	}
	if victim != "" {
		if r.persist != nil {
			r.persist.DeleteBan(ch.name, victim)
		}
		delete(ch.entries, victim)
	}
}

func (ch *channelState) activeBans(now time.Time) int {
	n := 0
	for _, e := range ch.entries {
		if e.banned(now) {
			n++
		}
	}
	return n
}

// Banned reports whether addr is currently sidelined for channel. This is on the
// selection hot path, so it takes only a read lock and does no allocation.
func (r *Registry) Banned(channel, addr string) bool {
	if channel == "" || addr == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.policy.Enabled {
		return false
	}
	ch, ok := r.chans[channel]
	if !ok {
		return false
	}
	e, ok := ch.entries[strings.ToLower(addr)]
	if !ok {
		return false
	}
	return e.banned(r.now())
}

// BanSet returns every active ban for a channel as addr -> expiry.
//
// Selection uses this instead of calling Banned per candidate: one lock
// acquisition for a whole pick, rather than one per proxy considered.
func (r *Registry) BanSet(channel string) map[string]time.Time {
	if channel == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.policy.Enabled {
		return nil
	}
	ch, ok := r.chans[channel]
	if !ok {
		return nil
	}
	now := r.now()
	out := make(map[string]time.Time, len(ch.entries))
	for addr, e := range ch.entries {
		if e.banned(now) {
			out[addr] = e.bannedUntil
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Channels summarizes every tracked channel, worst failure rate first.
func (r *Registry) Channels() []ChannelStat {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.now()
	bs := r.bucketSec()
	out := make([]ChannelStat, 0, len(r.chans))
	for name, ch := range r.chans {
		st := ChannelStat{
			Name:          name,
			Entries:       len(ch.entries),
			LastOutcomeAt: ch.lastSeenAt,
			LastBanAt:     ch.lastBanAt,
		}
		for _, e := range ch.entries {
			ok, fail, timeout := e.sum(now, bs)
			st.OK += ok
			st.Fail += fail
			st.Timeout += timeout
			if e.banned(now) {
				st.Bans++
			}
		}
		if total := st.OK + st.Fail; total > 0 {
			st.FailRate = float64(st.Fail) / float64(total)
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bans != out[j].Bans {
			return out[i].Bans > out[j].Bans
		}
		if out[i].FailRate != out[j].FailRate {
			return out[i].FailRate > out[j].FailRate
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Bans lists the active bans for one channel, soonest expiry first.
func (r *Registry) Bans(channel string) []Ban {
	channel = NormalizeChannelName(channel)
	r.mu.RLock()
	defer r.mu.RUnlock()
	ch, ok := r.chans[channel]
	if !ok {
		return nil
	}
	now := r.now()
	out := make([]Ban, 0, len(ch.entries))
	for addr, e := range ch.entries {
		if !e.banned(now) {
			continue
		}
		out = append(out, Ban{
			Channel:  channel,
			Addr:     addr,
			Reason:   e.banReason,
			BannedAt: e.lastBanAt,
			Until:    e.bannedUntil,
			Strikes:  e.strikes,
			TTLSec:   int(e.bannedUntil.Sub(e.lastBanAt) / time.Second),
			Pending:  e.pendingReprobe && (e.bannedUntil.IsZero() || !e.bannedUntil.After(now)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until.Before(out[j].Until) })
	return out
}

// Unban clears one ban. The strike ladder is kept so a manually released proxy
// that immediately misbehaves again is sidelined for longer, not from scratch.
func (r *Registry) Unban(channel, addr string) bool {
	channel = NormalizeChannelName(channel)
	addr = strings.ToLower(strings.TrimSpace(addr))
	r.mu.Lock()
	ch, ok := r.chans[channel]
	if !ok {
		r.mu.Unlock()
		return false
	}
	e, ok := ch.entries[addr]
	if !ok || (e.bannedUntil.IsZero() && !e.pendingReprobe) {
		r.mu.Unlock()
		return false
	}
	released := Ban{Channel: channel, Addr: addr, Reason: e.banReason, Until: e.bannedUntil, Strikes: e.strikes}
	e.bannedUntil = time.Time{}
	e.banReason = ""
	e.pendingReprobe = false
	e.consecFails = 0
	r.mu.Unlock()

	if r.persist != nil {
		r.persist.DeleteBan(channel, addr)
	}
	if r.onUnban != nil {
		r.onUnban(released)
	}
	return true
}

// ResetChannel clears every ban and counter for a channel but keeps the channel.
func (r *Registry) ResetChannel(channel string) bool {
	channel = NormalizeChannelName(channel)
	r.mu.Lock()
	ch, ok := r.chans[channel]
	if !ok {
		r.mu.Unlock()
		return false
	}
	ch.entries = map[string]*entry{}
	ch.lastBanAt = time.Time{}
	r.mu.Unlock()
	if r.persist != nil {
		r.persist.DeleteChannel(channel)
	}
	return true
}

// DeleteChannel removes the channel entirely.
func (r *Registry) DeleteChannel(channel string) bool {
	channel = NormalizeChannelName(channel)
	r.mu.Lock()
	_, ok := r.chans[channel]
	if ok {
		delete(r.chans, channel)
	}
	r.mu.Unlock()
	if ok && r.persist != nil {
		r.persist.DeleteChannel(channel)
	}
	return ok
}

// Sweep drops expired bans, entries that carry no live data, and channels left
// empty. Without it the maps only ever shrink through LRU pressure, so a pool
// that quiets down would hold its peak footprint indefinitely.
func (r *Registry) Sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	bs := r.bucketSec()
	idle := r.policy.idleResetAfter()
	for name, ch := range r.chans {
		for addr, e := range ch.entries {
			if e.banned(now) {
				continue
			}
			// Keep recently active entries: their strike ladder is still relevant.
			if !e.lastSeenAt.IsZero() && now.Sub(e.lastSeenAt) < idle {
				continue
			}
			if e.empty(now, bs) {
				delete(ch.entries, addr)
				if r.persist != nil {
					r.persist.DeleteBan(name, addr)
				}
			}
		}
		if len(ch.entries) == 0 && now.Sub(ch.lastSeenAt) > idle {
			delete(r.chans, name)
		}
	}
}

// Totals reports aggregate counts for /metrics.
func (r *Registry) Totals() (channels, bans int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.now()
	for _, ch := range r.chans {
		bans += ch.activeBans(now)
	}
	return len(r.chans), bans
}

// itoa avoids pulling strconv in for one call on the ban path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Logs returns the most recent outcomes, newest last. channel="" means all.
func (r *Registry) Logs(channel string, limit int) []LogEntry {
	if r == nil || r.log == nil {
		return []LogEntry{}
	}
	if channel != "" {
		channel = NormalizeChannelName(channel)
	}
	return r.log.list(channel, limit)
}

// ClearLogs drops the in-memory request log. Bans are not touched.
func (r *Registry) ClearLogs() {
	if r != nil && r.log != nil {
		r.log.clear()
	}
}

func (r *Registry) allowedLocked(channel, addr string) bool {
	if r.allows == nil {
		return false
	}
	if g := r.allows[""]; g != nil {
		if _, ok := g[addr]; ok {
			return true
		}
	}
	if c := r.allows[channel]; c != nil {
		if _, ok := c[addr]; ok {
			return true
		}
	}
	return false
}

// Allow protects addr from automatic bans. Empty channel = every channel.
func (r *Registry) Allow(channel, addr, reason string) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return
	}
	channel = NormalizeChannelName(channel)
	r.mu.Lock()
	if r.allows == nil {
		r.allows = map[string]map[string]Allow{}
	}
	if r.allows[channel] == nil {
		r.allows[channel] = map[string]Allow{}
	}
	r.allows[channel][addr] = Allow{Channel: channel, Addr: addr, Reason: reason, CreatedAt: r.now()}
	// A live ban on a newly protected pair is released: the operator just said
	// this IP must not be auto-banned.
	if ch, ok := r.chans[channel]; ok {
		if e, ok := ch.entries[addr]; ok {
			e.bannedUntil = time.Time{}
			e.pendingReprobe = false
			e.banReason = ""
		}
	}
	if channel == "" {
		for _, ch := range r.chans {
			if e, ok := ch.entries[addr]; ok {
				e.bannedUntil = time.Time{}
				e.pendingReprobe = false
				e.banReason = ""
			}
		}
	}
	r.mu.Unlock()
	if r.persist != nil {
		r.persist.SaveAllow(channel, addr, reason)
		r.persist.DeleteBan(channel, addr)
	}
}

func (r *Registry) Deny(channel, addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	channel = NormalizeChannelName(channel)
	r.mu.Lock()
	var ok bool
	if m := r.allows[channel]; m != nil {
		_, ok = m[addr]
		delete(m, addr)
	}
	r.mu.Unlock()
	if ok && r.persist != nil {
		r.persist.DeleteAllow(channel, addr)
	}
	return ok
}

func (r *Registry) Allows() []Allow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Allow, 0)
	for _, m := range r.allows {
		for _, a := range m {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Channel != out[j].Channel {
			return out[i].Channel < out[j].Channel
		}
		return out[i].Addr < out[j].Addr
	})
	return out
}

func (r *Registry) RestoreLogs(items []LogEntry) {
	if r == nil || r.log == nil {
		return
	}
	for _, e := range items {
		r.log.add(e)
	}
}

func (r *Registry) RestoreAllows(items []Allow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.allows == nil {
		r.allows = map[string]map[string]Allow{}
	}
	for _, a := range items {
		ch := NormalizeChannelName(a.Channel)
		addr := strings.ToLower(strings.TrimSpace(a.Addr))
		if addr == "" {
			continue
		}
		if r.allows[ch] == nil {
			r.allows[ch] = map[string]Allow{}
		}
		r.allows[ch][addr] = a
	}
}

func nextRuleID() string {
	return "r" + itoa(int(time.Now().UTC().UnixNano()%100_000_000))
}

// AddRule stores a custom ban condition. Empty Channel = every channel.
func (r *Registry) AddRule(in Rule) (Rule, error) {
	in, err := normalizeRule(in)
	if err != nil {
		return Rule{}, err
	}
	in.Enabled = true
	if in.CreatedAt.IsZero() {
		in.CreatedAt = r.now()
	}
	r.mu.Lock()
	if in.ID == "" {
		in.ID = nextRuleID()
	}
	replaced := false
	for i, existing := range r.rules {
		if existing.ID == in.ID {
			r.rules[i] = in
			replaced = true
			break
		}
	}
	if !replaced {
		r.rules = append(r.rules, in)
	}
	r.mu.Unlock()
	if r.persist != nil {
		r.persist.SaveRule(in)
	}
	return in, nil
}

func (r *Registry) DeleteRule(id string) bool {
	id = strings.TrimSpace(id)
	r.mu.Lock()
	kept := r.rules[:0]
	found := false
	for _, rule := range r.rules {
		if rule.ID == id {
			found = true
			continue
		}
		kept = append(kept, rule)
	}
	r.rules = kept
	r.mu.Unlock()
	if found && r.persist != nil {
		r.persist.DeleteRule(id)
	}
	return found
}

func (r *Registry) Rules() []Rule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Rule, len(r.rules))
	copy(out, r.rules)
	return out
}

func (r *Registry) RestoreRules(items []Rule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules = append([]Rule(nil), items...)
}
