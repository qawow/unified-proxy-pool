// Command discover probes candidate proxy-list URLs and reports which are worth
// adding to internal/crawlers/sources.go.
//
// It answers the three questions that decide whether a candidate earns a slot:
//
//  1. Does it return proxies at all? (many published lists are dead or 404)
//  2. Which parser format reads it best? (guessing wrong is how a source ends up
//     fetching bytes and yielding nothing — see the geonode/monosans case)
//  3. Is it actually new, or a mirror of a source already configured?
//
// Question 3 is the one that is easy to skip and expensive to get wrong: these
// GitHub lists copy each other constantly, so a 40k-entry "new" source can add
// zero addresses the pool did not already have.
//
//	# probe a candidate list, compare against what we already collect
//	go run ./cmd/fetchproxies -out /tmp/baseline.txt
//	go run ./cmd/discover -in scripts/source-candidates.txt -baseline /tmp/baseline.txt
//
//	# emit ready-to-paste Go declarations for the keepers
//	go run ./cmd/discover -in cand.txt -baseline /tmp/baseline.txt -emit-go
//
// Every parse goes through crawlers.NewDynamic, the same path the panel uses, so
// a format this tool reports as working will work in the panel too.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/freproxies"
)

// mirrorPrefix is the GitHub mirror sources.go uses. Candidates are retried
// through it when a direct fetch fails, since which one works is a property of
// the network the pool runs on, not of the source.
const mirrorPrefix = "https://gh.awa91.cyou/"

// candidate is one URL to probe, optionally with a hint from the input file.
type candidate struct {
	rawURL   string
	protoHnt string // protocol hint parsed from the URL or given inline
	nameHnt  string
}

// finding is what we learned about a candidate.
type finding struct {
	cand       candidate
	fetchedURL string // the URL that actually worked (may be the mirror)
	httpErr    error
	bytes      int
	bestFormat string
	proxies    []crawlers.Proxy
	byProto    map[string]int
	byFamily   map[string]int
	novel      int // addresses absent from the baseline
	// marginal is what this source adds beyond the baseline AND every
	// higher-ranked candidate. novel treats each candidate in isolation, which
	// makes two mirrors of one another both look valuable; marginal does not.
	marginal int
	scored   bool // whether marginal was computed
	elapsed  time.Duration
}

// verdict collapses a finding into the decision a maintainer has to make.
func (f finding) verdict(baselineLoaded bool) string {
	switch {
	case f.httpErr != nil:
		return "DEAD"
	case f.bytes == 0:
		return "EMPTY"
	case len(f.proxies) == 0:
		// Bytes arrived but nothing parsed: either not a proxy list, or a format
		// none of our parsers handles. Worth a human look, not an auto-add.
		return "UNPARSED"
	case baselineLoaded && f.novel == 0:
		return "DUPLICATE"
	case f.scored && f.marginal == 0:
		// Every address is already covered by the baseline or by a candidate
		// ranked above this one — typically a mirror of a bigger list.
		return "REDUNDANT"
	case f.scored && f.marginal*20 < len(f.proxies):
		// Under 5% incremental. Costs a fetch every round to re-learn what we
		// already have from elsewhere.
		return "MOSTLY-DUP"
	case baselineLoaded && !f.scored && f.novel*20 < len(f.proxies):
		return "MOSTLY-DUP"
	default:
		return "KEEP"
	}
}

// scoreMarginal computes each candidate's incremental contribution, processing
// the most novel first so the largest list claims shared addresses and its
// mirrors fall to zero.
//
// Without this, probing two lists that mirror each other reports both as
// valuable: SoliSpirit/proxy-list and MuRongPIG/Proxy-Master each scored ~74k
// novel against the baseline, but all 101,628 of MuRongPIG's addresses were
// already inside SoliSpirit's 122,777. Adding both doubles the per-round fetch
// cost for zero extra proxies.
func scoreMarginal(findings []finding, baseline map[string]struct{}) []finding {
	out := append([]finding(nil), findings...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].novel != out[j].novel {
			return out[i].novel > out[j].novel
		}
		return len(out[i].proxies) > len(out[j].proxies)
	})

	// claimed starts as the baseline and grows as candidates are accepted.
	claimed := make(map[string]struct{}, len(baseline)+1024)
	for addr := range baseline {
		claimed[addr] = struct{}{}
	}

	for i := range out {
		if out[i].httpErr != nil || len(out[i].proxies) == 0 {
			continue
		}
		gain := 0
		for _, p := range out[i].proxies {
			addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
			if _, known := claimed[addr]; known {
				continue
			}
			claimed[addr] = struct{}{}
			gain++
		}
		out[i].marginal = gain
		out[i].scored = true
	}
	return out
}

func main() {
	var (
		in          = flag.String("in", "", "file of candidate URLs (default stdin)")
		baselineIn  = flag.String("baseline", "", "file of addresses we already collect; enables novelty scoring")
		concurrency = flag.Int("concurrency", 6, "parallel probes")
		timeout     = flag.Duration("timeout", 25*time.Second, "per-request timeout")
		minProxies  = flag.Int("min", 20, "ignore candidates yielding fewer than this many proxies")
		emitGo      = flag.Bool("emit-go", false, "print sources.go declarations for the keepers")
		showAll     = flag.Bool("all", false, "list every candidate, not just keepers")
	)
	flag.Parse()

	cands, err := readCandidates(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read candidates: %v\n", err)
		os.Exit(1)
	}
	if len(cands) == 0 {
		fmt.Fprintln(os.Stderr, "no candidate URLs given")
		os.Exit(2)
	}

	baseline, err := readBaseline(*baselineIn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read baseline: %v\n", err)
		os.Exit(1)
	}
	if len(baseline) == 0 && *baselineIn != "" {
		fmt.Fprintf(os.Stderr, "warning: baseline %s held no addresses; novelty not scored\n", *baselineIn)
	}

	// Anything already in sources.go is not a discovery.
	configured := configuredURLs()
	fresh := make([]candidate, 0, len(cands))
	var skipped int
	for _, c := range cands {
		if configured[normalizeURLKey(c.rawURL)] {
			skipped++
			continue
		}
		fresh = append(fresh, c)
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "skipping %d candidate(s) already in sources.go\n", skipped)
	}
	if len(fresh) == 0 {
		fmt.Fprintln(os.Stderr, "every candidate is already configured")
		return
	}

	fmt.Fprintf(os.Stderr, "probing %d candidate(s), baseline holds %d address(es)\n\n",
		len(fresh), len(baseline))

	findings := probeAll(fresh, *concurrency, *timeout, baseline)
	// Score against each other, not just the baseline, so mirrors collapse.
	findings = scoreMarginal(findings, baseline)
	report(findings, len(baseline) > 0, *minProxies, *showAll)
	if *emitGo {
		emitGoDecls(findings, len(baseline) > 0, *minProxies)
	}
}

// readCandidates accepts "URL", "URL<space>protocol" and "name=URL" lines so a
// candidate list can carry what the URL alone does not say.
func readCandidates(path string) ([]candidate, error) {
	f := os.Stdin
	if path != "" {
		var err error
		f, err = os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
	}
	return readCandidatesFrom(f)
}

func readCandidatesFrom(r io.Reader) ([]candidate, error) {
	seen := map[string]struct{}{}
	out := make([]candidate, 0, 64)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var c candidate
		fields := strings.Fields(line)
		c.rawURL = fields[0]
		if i := strings.Index(c.rawURL, "="); i > 0 && !strings.Contains(c.rawURL[:i], "://") {
			c.nameHnt = c.rawURL[:i]
			c.rawURL = c.rawURL[i+1:]
		}
		if len(fields) > 1 {
			c.protoHnt = normalizeProtoHint(fields[1])
		}
		if c.protoHnt == "" {
			c.protoHnt = protoFromURL(c.rawURL)
		}
		if c.nameHnt == "" {
			c.nameHnt = nameFromURL(c.rawURL, c.protoHnt)
		}
		key := normalizeURLKey(c.rawURL)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	return out, sc.Err()
}

func readBaseline(path string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Tolerate "addr<tab>latency" from checkproxies -latency.
		out[strings.Fields(line)[0]] = struct{}{}
	}
	return out, sc.Err()
}

// configuredURLs indexes every URL already declared in sources.go, with and
// without the GitHub mirror prefix so a candidate cannot slip through by
// differing only in that.
func configuredURLs() map[string]bool {
	out := map[string]bool{}
	for _, c := range crawlers.DefaultSources() {
		for _, u := range c.URLs() {
			out[normalizeURLKey(u)] = true
		}
	}
	return out
}

// normalizeURLKey strips the mirror prefix and scheme so the same upstream file
// compares equal however it is reached.
func normalizeURLKey(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, mirrorPrefix)
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	return strings.ToLower(strings.TrimSuffix(raw, "/"))
}

func probeAll(cands []candidate, concurrency int, timeout time.Duration, baseline map[string]struct{}) []finding {
	if concurrency < 1 {
		concurrency = 1
	}
	client := crawlers.NewHTTPClient(timeout)
	out := make([]finding, len(cands))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, c := range cands {
		wg.Add(1)
		go func(i int, c candidate) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = probe(client, c, timeout, baseline)
			fmt.Fprintf(os.Stderr, "  %-52s %s\n", truncate(c.rawURL, 52), out[i].verdict(len(baseline) > 0))
		}(i, c)
	}
	wg.Wait()
	return out
}

func probe(client *crawlers.HTTPClient, c candidate, timeout time.Duration, baseline map[string]struct{}) finding {
	f := finding{cand: c, byProto: map[string]int{}, byFamily: map[string]int{}}
	start := time.Now()
	defer func() { f.elapsed = time.Since(start) }()

	body, used, err := fetchWithFallback(client, c.rawURL, timeout)
	f.fetchedURL = used
	if err != nil {
		f.httpErr = err
		return f
	}
	f.bytes = len(body)
	if f.bytes == 0 {
		return f
	}

	f.bestFormat, f.proxies = bestParse(body, c.protoHnt)
	for _, p := range f.proxies {
		f.byProto[p.Protocol]++
		f.byFamily[freproxies.DetectFamily(p.Host)]++
		if len(baseline) > 0 {
			if _, known := baseline[net.JoinHostPort(p.Host, strconv.Itoa(p.Port))]; !known {
				f.novel++
			}
		}
	}
	return f
}

// fetchWithFallback tries the URL as given, then through the GitHub mirror.
// Which one reaches a raw.githubusercontent.com file depends on the network the
// pool runs on, so testing only the direct form would reject usable sources.
func fetchWithFallback(client *crawlers.HTTPClient, raw string, timeout time.Duration) ([]byte, string, error) {
	attempts := []string{raw}
	if strings.Contains(raw, "raw.githubusercontent.com") && !strings.HasPrefix(raw, mirrorPrefix) {
		attempts = append(attempts, mirrorPrefix+raw)
	}
	var lastErr error
	for _, attempt := range attempts {
		ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
		body, err := client.Get(ctx, attempt)
		cancel()
		if err == nil {
			return body, attempt, nil
		}
		lastErr = err
	}
	return nil, raw, lastErr
}

// bestParse runs every plausible format and keeps the one yielding the most
// proxies. Guessing the format by URL extension is unreliable — sources serve
// JSON from .txt paths and plain lists from .json paths — and picking wrong is
// exactly the failure that left geonode and monosans-json silent.
func bestParse(body []byte, protoHint string) (string, []crawlers.Proxy) {
	if protoHint == "" {
		protoHint = "http"
	}
	bestFormat, bestItems := "", []crawlers.Proxy(nil)
	// plaintext first, and later formats must strictly beat it to win.
	//
	// dynamicCrawler.Parse falls back to ExtractIPPort for json, jsonl and
	// html_table when the structured parse finds nothing, so on a plain
	// host:port list every format returns an identical count. Ordering the
	// permissive format first turns that tie into the truthful answer; the
	// earlier order reported every .txt list as "json", which would have written
	// JSONSource into sources.go for plain text files.
	for _, format := range []string{"plaintext", "json", "jsonl", "html_table"} {
		c, err := crawlers.NewDynamic(crawlers.DynamicSpec{
			Name: "probe", URLs: []string{"http://probe.invalid"},
			Format: format, Protocol: protoHint,
			HostCol: 0, PortCol: 1,
		})
		if err != nil {
			continue
		}
		items, err := c.Parse(body, "http://probe.invalid")
		if err != nil {
			continue
		}
		if len(items) > len(bestItems) {
			bestFormat, bestItems = format, items
		}
	}
	return bestFormat, bestItems
}

func report(findings []finding, scored bool, minProxies int, showAll bool) {
	// scoreMarginal already ordered these by novelty, and marginal values depend
	// on that order — re-sorting by a different key here would print numbers
	// that no longer match the ranking they were computed under.
	counts := map[string]int{}
	fmt.Printf("\n%-44s %-11s %-10s %8s %8s %8s\n",
		"URL", "VERDICT", "FORMAT", "PROXIES", "NEW", "ADDS")
	fmt.Println(strings.Repeat("-", 95))

	for _, f := range findings {
		v := f.verdict(scored)
		if v == "KEEP" && len(f.proxies) < minProxies {
			v = "TOO-SMALL"
		}
		counts[v]++
		if !showAll && v != "KEEP" {
			continue
		}
		newCol, addsCol := "-", "-"
		if scored {
			newCol = strconv.Itoa(f.novel)
		}
		if f.scored {
			addsCol = strconv.Itoa(f.marginal)
		}
		fmt.Printf("%-44s %-11s %-10s %8d %8s %8s\n",
			truncate(f.cand.rawURL, 44), v, f.bestFormat, len(f.proxies), newCol, addsCol)
		if v == "KEEP" {
			fmt.Printf("      protocols=%s families=%s\n",
				renderCounts(f.byProto), renderCounts(f.byFamily))
		}
		if f.httpErr != nil && showAll {
			fmt.Printf("      error: %s\n", truncate(f.httpErr.Error(), 78))
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
	fmt.Printf("%d candidate(s): %s\n", len(findings), strings.Join(parts, " "))
	fmt.Println("\nNEW  = addresses absent from the baseline (each candidate judged alone)")
	fmt.Println("ADDS = addresses no higher-ranked candidate already covers — the honest gain.")
	fmt.Println("       A mirror of a bigger list has a high NEW and an ADDS near zero.")
	if !scored {
		fmt.Println("\nNo -baseline given, so DUPLICATE was not detected. To score against the live pool:")
		fmt.Println("  go run ./cmd/fetchproxies -out /tmp/baseline.txt")
		fmt.Println("  go run ./cmd/discover -in <candidates> -baseline /tmp/baseline.txt")
	}
	if counts["UNPARSED"] > 0 {
		fmt.Println("\nUNPARSED means bytes arrived but no parser read them. Re-run with -all to see them,")
		fmt.Println("then try one by hand: BODY_FILE=<saved> FORMAT=<fmt> go run ./cmd/testsource")
	}
}

// emitGoDecls prints declarations in the exact shape sources.go uses, including
// the measured yield as a comment — future maintainers need that number to tell
// a source that dried up from one that never worked.
func emitGoDecls(findings []finding, scored bool, minProxies int) {
	keepers := make([]finding, 0, len(findings))
	for _, f := range findings {
		if f.verdict(scored) == "KEEP" && len(f.proxies) >= minProxies {
			keepers = append(keepers, f)
		}
	}
	if len(keepers) == 0 {
		return
	}
	fmt.Printf("\n// ---- paste into DefaultSources() in internal/crawlers/sources.go ----\n")
	fmt.Printf("// Probed %s. Counts are yield at probe time.\n", time.Now().Format("2006-01-02"))
	for _, f := range keepers {
		ctor := "PlainText"
		switch f.bestFormat {
		case "json", "jsonl":
			ctor = "JSONSource"
		case "html_table":
			ctor = "HTMLTable"
		}
		proto := f.cand.protoHnt
		if proto == "" {
			proto = "http"
		}
		newNote := ""
		if scored {
			newNote = fmt.Sprintf(", %d new vs baseline", f.novel)
		}
		fmt.Printf("// %s: %d proxies%s\n", f.cand.nameHnt, len(f.proxies), newNote)

		urlArg := ghWrap(f.fetchedURL)
		if ctor == "HTMLTable" {
			// HTMLTable takes column indexes instead of a protocol.
			fmt.Printf("%s(%q, []string{%s}, 0, 1, true, true),\n", ctor, f.cand.nameHnt, urlArg)
			continue
		}
		fmt.Printf("%s(%q, []string{%s}, %q, false, true),\n", ctor, f.cand.nameHnt, urlArg, proto)
	}
}

// ghWrap renders a raw.githubusercontent.com URL as the gh() helper call that
// sources.go uses, so pasted lines match the surrounding style.
func ghWrap(raw string) string {
	trimmed := strings.TrimPrefix(raw, mirrorPrefix)
	const rawGH = "https://raw.githubusercontent.com/"
	if strings.HasPrefix(trimmed, rawGH) {
		return fmt.Sprintf("gh(%q)", strings.TrimPrefix(trimmed, rawGH))
	}
	return fmt.Sprintf("%q", trimmed)
}

// protoFromURL reads the protocol from the path and query, which is how nearly
// every published list names its files.
//
// The scheme and host are deliberately excluded: "https://x/proxies.txt" would
// otherwise match on its own scheme and report http for a list whose protocol is
// actually unknown, and a host like "socks5.example.com" would override the path.
func protoFromURL(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(lower, "://"); i >= 0 {
		lower = lower[i+3:]
	}
	// Drop the host: only the path and query describe the content.
	if i := strings.IndexAny(lower, "/?#"); i >= 0 {
		lower = lower[i:]
	} else {
		return ""
	}
	// Check the more specific tokens first: "socks5" contains "socks".
	for _, marker := range []string{"socks5", "socks4", "https", "http", "socks"} {
		if strings.Contains(lower, marker) {
			return normalizeProtoHint(marker)
		}
	}
	return ""
}

func normalizeProtoHint(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "http":
		return "http"
	case "https":
		// The pool dials https upstreams as http proxies; sources.go follows the
		// same convention, so record the dialable protocol.
		return "http"
	case "socks5", "socks5h", "socks":
		return "socks5"
	case "socks4", "socks4a":
		return "socks4"
	default:
		return ""
	}
}

// nameFromURL builds a source name in the "<owner>-<proto>" style sources.go
// uses, so emitted declarations do not need renaming by hand.
func nameFromURL(raw, proto string) string {
	trimmed := strings.TrimPrefix(raw, mirrorPrefix)
	u, err := url.Parse(trimmed)
	if err != nil {
		return "candidate"
	}
	base := ""
	if u.Host == "raw.githubusercontent.com" {
		// /owner/repo/branch/path -> owner
		if parts := strings.Split(strings.Trim(u.Path, "/"), "/"); len(parts) > 0 {
			base = parts[0]
		}
	}
	if base == "" {
		host := strings.TrimPrefix(u.Host, "www.")
		if i := strings.Index(host, "."); i > 0 {
			host = host[:i]
		}
		base = host
	}
	base = sanitizeName(base)
	if base == "" {
		base = sanitizeName(path.Base(u.Path))
	}
	if proto != "" && proto != "http" {
		return base + "-" + proto
	}
	if proto == "http" {
		return base + "-http"
	}
	return base
}

func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
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
	return strings.Join(parts, ",")
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
