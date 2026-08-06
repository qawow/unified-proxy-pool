// Command scanproxies scans proxies and writes them straight into the pool's
// store, without a running panel.
//
// This is the bulk path. scripts/import-proxies.sh goes through the panel's HTTP
// API at one request per address, which is fine for a few hundred; this writes
// via the store's own AddRaw/MarkValidated in batches, so tens of thousands are
// practical.
//
//	go run ./cmd/scanproxies                       # dry run: scan, report, write nothing
//	go run ./cmd/scanproxies -write                # commit to the store
//	go run ./cmd/scanproxies -in live.txt -write    # from a file instead of scraping
//	go run ./cmd/scanproxies -redis-db 15 -write   # target a scratch DB
//	go run ./cmd/scanproxies -skip-validate -write  # write raw, let the panel validate
//
// It writes to shared state, so -write is opt-in: the default run reports what
// it would do and changes nothing.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/freproxies"
)

// checked is one validated candidate awaiting a write.
type checked struct {
	proxy     freproxies.Proxy
	alive     bool
	latencyMS int64
}

type options struct {
	in            string
	sourceNames   string
	allSources    bool
	family        string
	protoOverride string
	validateURL   string
	timeout       time.Duration
	concurrency   int
	limit         int
	skipValidate  bool
	write         bool
	redisAddr     string
	redisPassword string
	redisDB       int
	sourceLabel   string
}

func main() {
	var o options
	flag.StringVar(&o.in, "in", "", "read addresses from this file instead of scraping (\"-\" for stdin)")
	flag.StringVar(&o.sourceNames, "name", "", "comma-separated source names to scrape")
	flag.BoolVar(&o.allSources, "all", false, "scrape sources that are disabled by default too")
	flag.StringVar(&o.family, "family", "", "only scan this IP family (ipv4, ipv6)")
	flag.StringVar(&o.protoOverride, "proto", "", "dial every address as this protocol (http, socks4, socks5)")
	flag.StringVar(&o.validateURL, "url", "https://www.gstatic.com/generate_204", "URL to fetch through each proxy")
	flag.DurationVar(&o.timeout, "timeout", 8*time.Second, "per-proxy validation timeout")
	flag.IntVar(&o.concurrency, "concurrency", 100, "parallel validations")
	flag.IntVar(&o.limit, "limit", 0, "stop after this many candidates (0 = no limit)")
	flag.BoolVar(&o.skipValidate, "skip-validate", false, "write without validating; the panel's validator will score them")
	flag.BoolVar(&o.write, "write", false, "actually write to the store (default is a dry run)")
	flag.StringVar(&o.redisAddr, "redis", envOr("REDIS_ADDR", "127.0.0.1:6379"), "Redis address")
	flag.StringVar(&o.redisPassword, "redis-password", os.Getenv("REDIS_PASSWORD"), "Redis password")
	flag.IntVar(&o.redisDB, "redis-db", envInt("REDIS_DB", 0), "Redis DB number")
	flag.StringVar(&o.sourceLabel, "source", "", "source label recorded on each proxy (default: scraper name, or \"scan\")")
	flag.Parse()

	candidates, err := collectCandidates(o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "collect: %v\n", err)
		os.Exit(1)
	}
	if len(candidates) == 0 {
		fmt.Fprintln(os.Stderr, "nothing to scan")
		os.Exit(2)
	}
	if o.limit > 0 && len(candidates) > o.limit {
		candidates = candidates[:o.limit]
	}

	results := candidates
	if !o.skipValidate {
		fmt.Fprintf(os.Stderr, "\nvalidating %d candidate(s) via %s (concurrency %d, timeout %s)\n",
			len(candidates), o.validateURL, o.concurrency, o.timeout)
		results = validateAll(candidates, o)
	} else {
		fmt.Fprintf(os.Stderr, "\n-skip-validate: %d candidate(s) will be written unvalidated\n", len(candidates))
	}

	summarize(results, o.skipValidate)

	if !o.write {
		fmt.Fprintln(os.Stderr, "\nDRY RUN — nothing was written. Re-run with -write to commit.")
		return
	}
	if err := commit(results, o); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return def
	}
	return n
}

// collectCandidates gathers addresses either from a file or by scraping, and
// applies the family filter. Scraped proxies keep their source name so the
// pool's per-source quality stats stay meaningful; file input has no source, so
// it gets a label.
func collectCandidates(o options) ([]checked, error) {
	if o.in != "" {
		items, err := readAddrFile(o.in)
		if err != nil {
			return nil, err
		}
		label := o.sourceLabel
		if label == "" {
			label = "scan"
		}
		return toCandidates(items, label, o), nil
	}
	return scrapeCandidates(o)
}

func readAddrFile(path string) ([]crawlers.Proxy, error) {
	f := os.Stdin
	if path != "-" {
		var err error
		f, err = os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
	}
	seen := map[string]struct{}{}
	out := make([]crawlers.Proxy, 0, 1024)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Tolerate "addr<tab>latency" from checkproxies -latency, and URL forms.
		line = strings.Fields(line)[0]
		if i := strings.Index(line, "://"); i >= 0 {
			line = line[i+3:]
		}
		host, portText, err := net.SplitHostPort(line)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, crawlers.Proxy{Host: host, Port: port})
	}
	return out, sc.Err()
}

// scrapeCandidates fetches from the built-in sources, keeping each proxy's
// source name so the pool can score sources independently.
func scrapeCandidates(o options) ([]checked, error) {
	names := map[string]bool{}
	for _, n := range strings.Split(o.sourceNames, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names[strings.ToLower(n)] = true
		}
	}
	selected := make([]crawlers.Crawler, 0, 64)
	for _, c := range crawlers.DefaultSources() {
		if len(names) > 0 {
			// Naming a source explicitly overrides its enabled/disabled default.
			if !names[strings.ToLower(c.Name())] {
				continue
			}
		} else if !o.allSources && !c.DefaultEnabled() {
			continue
		}
		selected = append(selected, c)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no sources matched")
	}

	fmt.Fprintf(os.Stderr, "scraping %d source(s)\n", len(selected))
	client := crawlers.NewHTTPClient(25 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var (
		mu   sync.Mutex
		out  []checked
		seen = map[string]struct{}{}
	)
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup

	for _, c := range selected {
		wg.Add(1)
		go func(c crawlers.Crawler) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			items, err := crawlers.FetchAll(ctx, client, c)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %-26s error: %v\n", c.Name(), err)
				return
			}
			label := o.sourceLabel
			if label == "" {
				label = c.Name()
			}
			cands := toCandidates(items, label, o)

			mu.Lock()
			defer mu.Unlock()
			kept := 0
			for _, cd := range cands {
				if _, dup := seen[cd.proxy.Addr]; dup {
					continue
				}
				seen[cd.proxy.Addr] = struct{}{}
				out = append(out, cd)
				kept++
			}
			fmt.Fprintf(os.Stderr, "  %-26s %d proxies (%d new)\n", c.Name(), len(items), kept)
		}(c)
	}
	wg.Wait()
	return out, nil
}

// toCandidates normalizes crawler output into store-ready proxies, applying the
// family filter and protocol override.
func toCandidates(items []crawlers.Proxy, source string, o options) []checked {
	out := make([]checked, 0, len(items))
	for _, item := range items {
		if o.family != "" && !strings.EqualFold(freproxies.DetectFamily(item.Host), o.family) {
			continue
		}
		proto := item.Protocol
		if o.protoOverride != "" {
			proto = o.protoOverride
		}
		if proto == "" {
			proto = "http"
		}
		out = append(out, checked{proxy: freproxies.Proxy{
			// Addr is what the store keys on; JoinHostPort brackets IPv6 to match.
			Addr:     net.JoinHostPort(item.Host, strconv.Itoa(item.Port)),
			Host:     item.Host,
			Port:     item.Port,
			Protocol: proto,
			Source:   source,
			IPFamily: freproxies.DetectFamily(item.Host),
		}})
	}
	return out
}

// validateAll dials every candidate with the pool's own check, so a proxy this
// marks alive is one the panel would also accept.
func validateAll(cands []checked, o options) []checked {
	if o.concurrency < 1 {
		o.concurrency = 1
	}
	out := make([]checked, len(cands))
	copy(out, cands)

	var done int64
	sem := make(chan struct{}, o.concurrency)
	var wg sync.WaitGroup

	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// The outer ceiling only stops a pathological hang; CheckProxy
			// applies its own timeout.
			ctx, cancel := context.WithTimeout(context.Background(), o.timeout+2*time.Second)
			defer cancel()

			ms, alive := freproxies.CheckProxy(ctx, out[i].proxy, o.validateURL, o.timeout)
			out[i].alive = alive
			out[i].latencyMS = ms

			if n := atomic.AddInt64(&done, 1); n%500 == 0 {
				fmt.Fprintf(os.Stderr, "  %d/%d validated\n", n, len(out))
			}
		}(i)
	}
	wg.Wait()
	return out
}

func summarize(results []checked, skipValidate bool) {
	bySource := map[string]int{}
	byFamily := map[string]int{}
	byProto := map[string]int{}
	alive := 0
	var latencySum int64

	for _, c := range results {
		if skipValidate || c.alive {
			bySource[c.proxy.Source]++
			byFamily[c.proxy.Family()]++
			byProto[c.proxy.Protocol]++
		}
		if c.alive {
			alive++
			latencySum += c.latencyMS
		}
	}

	fmt.Fprintln(os.Stderr)
	if skipValidate {
		fmt.Fprintf(os.Stderr, "%d candidate(s) to write (unvalidated)\n", len(results))
	} else {
		rate := 0.0
		if len(results) > 0 {
			rate = float64(alive) / float64(len(results)) * 100
		}
		fmt.Fprintf(os.Stderr, "%d/%d alive (%.1f%%)\n", alive, len(results), rate)
		if alive > 0 {
			fmt.Fprintf(os.Stderr, "mean latency %dms\n", latencySum/int64(alive))
		}
	}
	fmt.Fprintf(os.Stderr, "protocols: %s\n", renderCounts(byProto))
	fmt.Fprintf(os.Stderr, "families:  %s\n", renderCounts(byFamily))
	if len(bySource) > 1 {
		fmt.Fprintf(os.Stderr, "top sources: %s\n", topCounts(bySource, 8))
	}
}

func renderCounts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func topCounts(m map[string]int, n int) string {
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(m))
	for k, v := range m {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	if len(all) > n {
		all = all[:n]
	}
	parts := make([]string, 0, len(all))
	for _, e := range all {
		parts = append(parts, fmt.Sprintf("%s:%d", e.k, e.v))
	}
	return strings.Join(parts, " ")
}

// counts is a snapshot of the store's size, taken before and after a write.
type counts struct {
	total     int64
	validated int64
	raw       int64
}

// capNote explains a write that cost the pool proxies, returning "" when there
// is nothing surprising to report.
//
// Trim caps the raw pool at MaxRawProxies and evicts the lowest-scoring entries
// first. Every freshly scraped proxy enters at ScoreInit, so the eviction lands
// on a tie and effectively drops other sources' proxies at random. Writing 21
// proxies into an over-cap pool was measured shrinking the total by 383, which
// is the kind of result that has to be stated rather than left for the operator
// to notice in the panel later.
func capNote(before, after counts, scanned, added int) string {
	// Eviction has to be derived from what AddRaw actually inserted, not from the
	// raw delta: a measured run added 254 and promoted 42 while raw held at
	// exactly 4000, because the insertions and the evictions cancelled out. The
	// total rose by 42, so every delta-based check stayed quiet while 212
	// proxies were dropped.
	//
	// Conservation: every address added either sits in raw, moved to validated,
	// or was evicted.
	promoted := after.validated - before.validated
	evicted := (before.total + int64(added)) - after.total

	switch {
	case after.total < before.total:
		return fmt.Sprintf("the pool shrank by %d despite writing %d proxies.\n"+
			"Trim caps the raw pool at %d and drops the lowest-scoring entries; fresh proxies\n"+
			"all enter at the same score, so the eviction falls on ties and removes other\n"+
			"sources' proxies. Use -limit, or validate first and write only what is alive.",
			before.total-after.total, scanned, freproxies.MaxRawProxies)
	case scanned > freproxies.MaxRawProxies:
		return fmt.Sprintf("scanned %d proxies against a raw cap of %d, so Trim discarded the\n"+
			"excess. Split the scan with -limit if you need all of it retained.",
			scanned, freproxies.MaxRawProxies)
	case evicted > 0:
		return fmt.Sprintf("Trim evicted %d proxies to stay under the raw cap (%d).\n"+
			"The pool still grew by %d (%d promoted to validated), so the loss is easy to miss:\n"+
			"insertions and evictions cancel out in the raw count. Fresh proxies all enter at\n"+
			"the same score, so what gets dropped is effectively arbitrary across sources.",
			evicted, freproxies.MaxRawProxies, after.total-before.total, promoted)
	default:
		return ""
	}
}

// writeSet returns the proxies worth inserting.
//
// After validating, addresses measured dead are dropped rather than inserted.
// AddRaw admits them at ScoreInit, where they occupy raw-cap space and make Trim
// evict other sources' proxies — to store addresses we just proved do not work.
//
// Measured round that exposed this: 278 candidates, 42 alive, 236 dead. All 278
// were inserted, 225 counted as new, Trim evicted 225, and the pool netted
// total+0 — a round of pure churn.
//
// Without validation there is no verdict to act on, so everything is written and
// the panel's own validator scores it later.
func writeSet(results []checked, skipValidate bool) []freproxies.Proxy {
	out := make([]freproxies.Proxy, 0, len(results))
	for _, c := range results {
		if !skipValidate && !c.alive {
			continue
		}
		out = append(out, c.proxy)
	}
	return out
}

// commit writes to the store: AddRaw to insert, then MarkValidated to record
// each verdict. Both are the store's own methods, so the pool sees exactly what
// a scrape+validate round would have produced.
func commit(results []checked, o options) error {
	store, err := freproxies.OpenRedis(o.redisAddr, o.redisPassword, o.redisDB)
	if err != nil {
		return fmt.Errorf("open redis %s db %d: %w", o.redisAddr, o.redisDB, err)
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	bTotal, bValidated, bRaw, err := store.Count(ctx)
	if err != nil {
		return fmt.Errorf("count: %w", err)
	}
	before := counts{total: bTotal, validated: bValidated, raw: bRaw}
	fmt.Fprintf(os.Stderr, "\nwriting to redis %s db %d\n", o.redisAddr, o.redisDB)
	fmt.Fprintf(os.Stderr, "before: total=%d validated=%d raw=%d\n", before.total, before.validated, before.raw)

	// AddRaw in batches: one giant slice would build a single pipeline holding
	// every command, and AddRaw already self-trims past 500 additions.
	const batch = 500
	proxies := writeSet(results, o.skipValidate)
	if skipped := len(results) - len(proxies); skipped > 0 {
		fmt.Fprintf(os.Stderr, "skipping %d address(es) measured dead — inserting them would\n", skipped)
		fmt.Fprintf(os.Stderr, "consume raw-cap space and evict working proxies from other sources\n")
	}
	added := 0
	for start := 0; start < len(proxies); start += batch {
		end := start + batch
		if end > len(proxies) {
			end = len(proxies)
		}
		n, err := store.AddRaw(ctx, proxies[start:end])
		if err != nil {
			return fmt.Errorf("addraw batch at %d: %w", start, err)
		}
		added += n
	}
	fmt.Fprintf(os.Stderr, "AddRaw: %d new (the rest were already known)\n", added)

	if !o.skipValidate {
		// Record verdicts for every candidate, not only the new ones: a proxy the
		// pool already had still benefits from a fresh result, and a dead one
		// needs its score decremented.
		var promoted, failed int
		for _, c := range results {
			if err := store.MarkValidated(ctx, c.proxy.Addr, c.latencyMS, c.alive); err != nil {
				// A proxy evicted mid-run (3 strikes) is gone from the store, and
				// MarkValidated reports that as an error. Not fatal.
				continue
			}
			if c.alive {
				promoted++
			} else {
				failed++
			}
		}
		fmt.Fprintf(os.Stderr, "MarkValidated: %d alive promoted, %d dead recorded\n", promoted, failed)
	}

	if err := store.Trim(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: trim: %v\n", err)
	}

	aTotal, aValidated, aRaw, err := store.Count(ctx)
	if err != nil {
		return fmt.Errorf("count after: %w", err)
	}
	after := counts{total: aTotal, validated: aValidated, raw: aRaw}
	fmt.Fprintf(os.Stderr, "after:  total=%d validated=%d raw=%d\n", after.total, after.validated, after.raw)
	fmt.Fprintf(os.Stderr, "delta:  total%+d validated%+d raw%+d\n",
		after.total-before.total, after.validated-before.validated, after.raw-before.raw)

	if note := capNote(before, after, len(results), added); note != "" {
		fmt.Fprintf(os.Stderr, "\nnote: %s\n", note)
	}
	return nil
}
