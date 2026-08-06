// Command sourceyield measures how many *working* proxies each source actually
// contributes, and recommends what to enable or disable.
//
// Raw yield is a misleading signal. A source adding 28,633 addresses of which
// none work is worse than one adding 100 of which 40 work: both cost a fetch
// every round, but the first also fills the raw pool and pushes other sources'
// proxies out through Trim. The panel's own counters do not answer this — the
// `last_ok` on /api/scrapers counts addresses added, not addresses that work.
//
//	go run ./cmd/sourceyield                    # sample every enabled source
//	go run ./cmd/sourceyield -sample 300        # tighter confidence, slower
//	go run ./cmd/sourceyield -name a,b -v
//	go run ./cmd/sourceyield -emit-toggles      # commands to apply the advice
//
// Each source is sampled rather than fully validated: validating 45k addresses
// from one source would take hours. The report carries a confidence interval so
// a rate measured from 200 samples is not mistaken for a certainty.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/freproxies"
)

// yield is what a single source contributes, measured rather than assumed.
type yield struct {
	name     string
	proto    string
	format   string
	enabled  bool
	fetched  int // addresses the source published
	sampled  int // addresses actually dialed
	alive    int // of those, how many worked
	fetchErr error
	elapsed  time.Duration
}

// rate is the observed success ratio over the sample.
func (y yield) rate() float64 {
	if y.sampled == 0 {
		return 0
	}
	return float64(y.alive) / float64(y.sampled)
}

// rateCI returns a Wilson score interval for the observed rate.
//
// A plain ratio invites over-reading: 1 alive out of 5 sampled is "20%", which
// looks identical to 40 out of 200 but says almost nothing. Wilson behaves
// sensibly at small n and at rates near zero, which is where most free proxy
// sources sit.
func (y yield) rateCI() (low, high float64) {
	n := float64(y.sampled)
	if n == 0 {
		return 0, 0
	}
	const z = 1.96 // 95%
	p := y.rate()
	denom := 1 + z*z/n
	center := (p + z*z/(2*n)) / denom
	margin := (z / denom) * math.Sqrt(p*(1-p)/n+z*z/(4*n*n))
	return math.Max(0, center-margin), math.Min(1, center+margin)
}

// estimatedAlive projects the sample onto the source's full output. This is the
// number worth ranking on: it is what the source is expected to contribute per
// round.
func (y yield) estimatedAlive() float64 {
	return y.rate() * float64(y.fetched)
}

// advice is the recommended action, with the reasoning attached.
func (y yield) advice(minSample int) (action, why string) {
	switch {
	case y.fetchErr != nil:
		return "DISABLE", "fetch failed: " + truncate(y.fetchErr.Error(), 60)
	case y.fetched == 0:
		return "DISABLE", "published nothing"
	case y.sampled < minSample:
		// Too few samples to judge; saying so beats inventing a verdict.
		return "UNKNOWN", fmt.Sprintf("only %d sampled, need %d to judge", y.sampled, minSample)
	case y.alive == 0:
		_, high := y.rateCI()
		return "DISABLE", fmt.Sprintf("0/%d worked (true rate under %.1f%%)", y.sampled, high*100)
	case y.fetched > freproxies.MaxRawProxies:
		// Big and productive is still a problem: one round fills the raw pool and
		// Trim evicts other sources' proxies on score ties.
		return "OVERSIZED", fmt.Sprintf("%d addresses is over the raw cap (%d); keep it off or raise the cap",
			y.fetched, freproxies.MaxRawProxies)
	default:
		low, _ := y.rateCI()
		return "KEEP", fmt.Sprintf("%.1f%% alive (at least %.1f%%), ~%.0f working per round",
			y.rate()*100, low*100, y.estimatedAlive())
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func main() {
	var (
		sampleSize  = flag.Int("sample", 120, "addresses to dial per source")
		minSample   = flag.Int("min-sample", 20, "below this many samples, report UNKNOWN instead of a verdict")
		wantNames   = flag.String("name", "", "comma-separated source names (default: all enabled)")
		allSources  = flag.Bool("all", false, "include sources disabled by default")
		validateURL = flag.String("url", "https://www.gstatic.com/generate_204", "URL to fetch through each proxy")
		timeout     = flag.Duration("timeout", 8*time.Second, "per-proxy timeout")
		concurrency = flag.Int("concurrency", 60, "parallel dials within a source")
		emitToggles = flag.Bool("emit-toggles", false, "print curl commands to apply the advice")
		panel       = flag.String("panel", envOr("UPP_PANEL", "http://127.0.0.1:7891"), "panel URL used in emitted commands")
		seed        = flag.Int64("seed", 0, "sampling seed (0 = time-based)")
		verbose     = flag.Bool("v", false, "log each source as it finishes")
		persist     = flag.Bool("persist", false, "save measurements to the store for trend analysis")
		redisAddr   = flag.String("redis", envOr("REDIS_ADDR", "127.0.0.1:6379"), "Redis address")
		redisPass   = flag.String("redis-password", os.Getenv("REDIS_PASSWORD"), "Redis password")
		redisDB     = flag.Int("redis-db", envInt("REDIS_DB", 0), "Redis DB number")
	)
	flag.Parse()

	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	fmt.Fprintf(os.Stderr, "sampling seed %d (pass -seed %d to reproduce)\n", *seed, *seed)

	selected := selectSources(*wantNames, *allSources)
	if len(selected) == 0 {
		fmt.Fprintln(os.Stderr, "no sources matched")
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "measuring %d source(s), up to %d dials each\n\n", len(selected), *sampleSize)

	results := measureAll(selected, *sampleSize, *validateURL, *timeout, *concurrency, *seed, *verbose)
	report(results, *minSample)
	if *emitToggles {
		emitToggleCmds(results, *minSample, strings.TrimSuffix(*panel, "/"))
	}
	if *persist {
		// The count reported is what was actually stored, not len(results):
		// sources whose fetch failed carry no measurement and are dropped.
		records := yieldRecords(results, *minSample, time.Now().UTC())
		store, err := openYieldStore(*redisAddr, *redisPass, *redisDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nwarning: failed to persist results: %v\n", err)
			return
		}
		defer store.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := persistResults(ctx, store, records); err != nil {
			fmt.Fprintf(os.Stderr, "\nwarning: failed to persist results: %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "\nstored %d measurement(s) in redis %s db %d",
			len(records), *redisAddr, *redisDB)
		if skipped := len(results) - len(records); skipped > 0 {
			fmt.Fprintf(os.Stderr, " (%d source(s) skipped: fetch failed, nothing measured)", skipped)
		}
		fmt.Fprintln(os.Stderr)
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

func selectSources(wantNames string, all bool) []crawlers.Crawler {
	names := map[string]bool{}
	for _, n := range strings.Split(wantNames, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names[strings.ToLower(n)] = true
		}
	}
	out := make([]crawlers.Crawler, 0, 64)
	for _, c := range crawlers.DefaultSources() {
		if len(names) > 0 {
			if !names[strings.ToLower(c.Name())] {
				continue
			}
		} else if !all && !c.DefaultEnabled() {
			continue
		}
		out = append(out, c)
	}
	return out
}

// measureAll fetches and samples each source. Sources run one at a time so a
// source's measured rate is not depressed by another source saturating the
// network at the same moment — the whole point is comparing rates fairly.
func measureAll(sources []crawlers.Crawler, sampleSize int, validateURL string,
	timeout time.Duration, concurrency int, seed int64, verbose bool) []yield {

	client := crawlers.NewHTTPClient(25 * time.Second)
	rng := rand.New(rand.NewSource(seed))
	out := make([]yield, 0, len(sources))

	for _, c := range sources {
		start := time.Now()
		y := yield{
			name:    c.Name(),
			proto:   c.Protocol(),
			format:  crawlers.CrawlerFormat(c),
			enabled: c.DefaultEnabled(),
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		items, err := crawlers.FetchAll(ctx, client, c)
		cancel()
		if err != nil {
			y.fetchErr = err
			y.elapsed = time.Since(start)
			out = append(out, y)
			if verbose {
				fmt.Fprintf(os.Stderr, "  %-26s fetch error: %v\n", y.name, err)
			}
			continue
		}
		y.fetched = len(items)

		sample := pickSample(items, sampleSize, rng)
		y.sampled = len(sample)
		y.alive = countAlive(sample, c.Protocol(), validateURL, timeout, concurrency)
		y.elapsed = time.Since(start)
		out = append(out, y)

		if verbose {
			fmt.Fprintf(os.Stderr, "  %-26s %6d fetched, %3d/%3d alive (%s)\n",
				y.name, y.fetched, y.alive, y.sampled, y.elapsed.Round(time.Second))
		}
	}
	return out
}

// pickSample takes a random subset. Random rather than the first N: these lists
// are often ordered, so the head is not representative of the tail.
func pickSample(items []crawlers.Proxy, n int, rng *rand.Rand) []crawlers.Proxy {
	if len(items) <= n {
		return items
	}
	idx := rng.Perm(len(items))[:n]
	out := make([]crawlers.Proxy, 0, n)
	for _, i := range idx {
		out = append(out, items[i])
	}
	return out
}

func countAlive(sample []crawlers.Proxy, proto, validateURL string,
	timeout time.Duration, concurrency int) int {

	if concurrency < 1 {
		concurrency = 1
	}
	var (
		mu    sync.Mutex
		alive int
		wg    sync.WaitGroup
	)
	sem := make(chan struct{}, concurrency)

	for _, item := range sample {
		wg.Add(1)
		go func(item crawlers.Proxy) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			itemProto := item.Protocol
			if itemProto == "" {
				itemProto = proto
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout+2*time.Second)
			defer cancel()

			_, ok := freproxies.CheckProxy(ctx, freproxies.Proxy{
				Host: item.Host, Port: item.Port, Protocol: itemProto,
			}, validateURL, timeout)
			if !ok {
				return
			}
			mu.Lock()
			alive++
			mu.Unlock()
		}(item)
	}
	wg.Wait()
	return alive
}

func report(results []yield, minSample int) {
	// Rank by projected working proxies per round, which is the contribution the
	// pool actually feels. Ranking by raw yield would put a 45k-address source
	// with a 0% rate at the top.
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].estimatedAlive() > results[j].estimatedAlive()
	})

	fmt.Printf("\n%-26s %-8s %-10s %8s %9s %8s %-10s\n",
		"SOURCE", "PROTO", "FORMAT", "FETCHED", "ALIVE", "EST/RND", "ADVICE")
	fmt.Println(strings.Repeat("-", 96))

	counts := map[string]int{}
	var totalEst float64
	for _, y := range results {
		action, why := y.advice(minSample)
		counts[action]++
		totalEst += y.estimatedAlive()

		aliveCol := fmt.Sprintf("%d/%d", y.alive, y.sampled)
		if y.fetchErr != nil {
			aliveCol = "-"
		}
		fmt.Printf("%-26s %-8s %-10s %8d %9s %8.0f %-10s\n",
			truncate(y.name, 26), y.proto, y.format, y.fetched, aliveCol, y.estimatedAlive(), action)
		if action != "KEEP" {
			fmt.Printf("      %s\n", why)
		}
	}

	fmt.Println()
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	fmt.Printf("%d source(s): %s\n", len(results), strings.Join(parts, " "))
	fmt.Printf("projected working proxies per round: ~%.0f\n", totalEst)
	fmt.Println("\nEST/RND is the sampled rate applied to the source's full output — rank on this,")
	fmt.Println("not on FETCHED. A source can publish tens of thousands of dead addresses, which")
	fmt.Println("costs a fetch every round and pushes other sources out of the raw pool via Trim.")
	if counts["UNKNOWN"] > 0 {
		fmt.Printf("\nUNKNOWN means too few addresses to judge. Raise -sample, or accept that a\n")
		fmt.Println("source this small cannot be measured confidently.")
	}
}

// emitToggleCmds prints the API calls that would apply the advice. It prints
// them rather than running them: toggling a source changes shared panel state,
// and the operator should see the list before any of it takes effect.
func emitToggleCmds(results []yield, minSample int, panel string) {
	type cmd struct{ name, why string }
	var off []cmd
	for _, y := range results {
		action, why := y.advice(minSample)
		if action == "DISABLE" && y.enabled {
			off = append(off, cmd{y.name, why})
		}
	}
	if len(off) == 0 {
		fmt.Println("\nNo toggles suggested: every enabled source is contributing.")
		return
	}
	fmt.Printf("\n# ---- suggested toggles (review before running) ----\n")
	fmt.Printf("# Needs a session cookie: log in first, then reuse the jar.\n")
	fmt.Printf("#   curl -c /tmp/jar -H 'Content-Type: application/json' \\\n")
	fmt.Printf("#        -d '{\"password\":\"…\"}' %s/api/auth/login\n", panel)
	for _, c := range off {
		fmt.Printf("# %s: %s\n", c.name, c.why)
		fmt.Printf("curl -sS -b /tmp/jar -X POST %s/api/scrapers/%s/toggle\n", panel, c.name)
	}
	fmt.Printf("\n# Toggle is a flip, not a set: running one of these twice re-enables the source.\n")
}

// yieldRecords converts measurements into the records worth storing. It touches
// no I/O so a test can check what would be written without opening a connection.
//
// Sources whose fetch failed are dropped: there is no measurement behind them,
// and recording a fetch failure as "0 alive" would make a temporarily
// unreachable source look permanently dead to trend analysis.
func yieldRecords(results []yield, minSample int, now time.Time) []freproxies.SourceYieldRecord {
	out := make([]freproxies.SourceYieldRecord, 0, len(results))
	for _, y := range results {
		if y.fetchErr != nil {
			continue
		}
		action, why := y.advice(minSample)
		out = append(out, freproxies.SourceYieldRecord{
			Source:    y.name,
			MeasureAt: now,
			Fetched:   y.fetched,
			Sampled:   y.sampled,
			Alive:     y.alive,
			Estimate:  int(y.estimatedAlive()),
			Action:    action,
			Why:       why,
		})
	}
	return out
}

// persistResults writes measurements through an already-open store.
//
// It takes a Store rather than connection details on purpose. The earlier
// signature accepted (addr, password, db) and dialed internally, so a test could
// only exercise it by connecting to something — and go-redis maps an empty Addr
// to localhost:6379 db 0, which is the live pool. A test passing "" wrote
// fixture rows named "dead" and "good" straight into production.
func persistResults(ctx context.Context, store freproxies.Store, records []freproxies.SourceYieldRecord) error {
	for _, rec := range records {
		if err := store.SaveSourceYield(ctx, rec); err != nil {
			return fmt.Errorf("save %s: %w", rec.Source, err)
		}
	}
	return nil
}

// openYieldStore dials Redis for the -persist path.
//
// An empty address is rejected rather than defaulted: go-redis would silently
// substitute localhost:6379 db 0, turning a misconfigured flag into a write to
// the live pool.
func openYieldStore(addr, password string, db int) (freproxies.Store, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, fmt.Errorf("-persist needs a Redis address (-redis or REDIS_ADDR); " +
			"refusing to fall back to localhost:6379, which is the live pool")
	}
	return freproxies.OpenRedis(addr, password, db)
}
