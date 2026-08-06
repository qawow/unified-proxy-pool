// Command fetchproxies pulls proxies from the built-in sources using the same
// crawler code the panel runs, so what it prints is exactly what a scrape round
// would ingest — no second parser to drift out of sync.
//
//	go run ./cmd/fetchproxies                        # default-enabled sources
//	go run ./cmd/fetchproxies -all                   # every source, incl. fragile
//	go run ./cmd/fetchproxies -proto socks5 -out s5.txt
//	go run ./cmd/fetchproxies -family ipv6 -stats
//	go run ./cmd/fetchproxies -name geonode,fatezero -v
//
// Sources that return a body but yield no proxies are reported as SILENT: that
// is what a broken parser or a changed response format looks like from outside.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/freproxies"
)

type result struct {
	name    string
	proto   string
	format  string
	items   []crawlers.Proxy
	err     error
	elapsed time.Duration
}

func main() {
	var (
		all         = flag.Bool("all", false, "include sources that are disabled by default")
		wantProto   = flag.String("proto", "", "only this protocol (http, https, socks4, socks5)")
		wantFormat  = flag.String("format", "", "only this parser format (plaintext, json, html_table, regex)")
		wantNames   = flag.String("name", "", "comma-separated source names")
		wantFamily  = flag.String("family", "", "only this IP family (ipv4, ipv6)")
		concurrency = flag.Int("concurrency", 8, "parallel fetches")
		timeout     = flag.Duration("timeout", 20*time.Second, "per-request timeout")
		total       = flag.Duration("deadline", 5*time.Minute, "overall deadline")
		out         = flag.String("out", "", "write addresses here instead of stdout")
		stats       = flag.Bool("stats", false, "per-source counts on stderr")
		verbose     = flag.Bool("v", false, "log each source as it finishes")
	)
	flag.Parse()

	selected := selectSources(*all, *wantProto, *wantFormat, *wantNames)
	if len(selected) == 0 {
		fmt.Fprintln(os.Stderr, "no sources matched the given filters")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *total)
	defer cancel()

	results := fetchAll(ctx, selected, *concurrency, *timeout, *verbose)
	addrs, kept := collect(results, *wantFamily)

	if err := writeAddrs(*out, addrs); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	report(results, len(addrs), kept, *stats)
}

func selectSources(all bool, wantProto, wantFormat, wantNames string) []crawlers.Crawler {
	names := map[string]bool{}
	for _, n := range strings.Split(wantNames, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names[strings.ToLower(n)] = true
		}
	}
	out := make([]crawlers.Crawler, 0, 64)
	for _, c := range crawlers.DefaultSources() {
		if len(names) > 0 {
			// An explicit name list overrides the enabled/disabled default:
			// asking for a source by name means you want it fetched.
			if !names[strings.ToLower(c.Name())] {
				continue
			}
		} else if !all && !c.DefaultEnabled() {
			continue
		}
		if wantProto != "" && !strings.EqualFold(c.Protocol(), wantProto) {
			continue
		}
		if wantFormat != "" && !strings.EqualFold(crawlers.CrawlerFormat(c), wantFormat) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func fetchAll(ctx context.Context, sources []crawlers.Crawler, concurrency int, timeout time.Duration, verbose bool) []result {
	if concurrency < 1 {
		concurrency = 1
	}
	client := crawlers.NewHTTPClient(timeout)
	results := make([]result, len(sources))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, c := range sources {
		wg.Add(1)
		go func(i int, c crawlers.Crawler) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			start := time.Now()
			items, err := crawlers.FetchAll(ctx, client, c)
			results[i] = result{
				name:    c.Name(),
				proto:   c.Protocol(),
				format:  crawlers.CrawlerFormat(c),
				items:   items,
				err:     err,
				elapsed: time.Since(start),
			}
			if verbose {
				status := fmt.Sprintf("%d proxies", len(items))
				if err != nil {
					status = "error: " + err.Error()
				}
				fmt.Fprintf(os.Stderr, "  %-26s %-8s %s (%s)\n",
					c.Name(), c.Protocol(), status, time.Since(start).Round(time.Millisecond))
			}
		}(i, c)
	}
	wg.Wait()
	return results
}

// collect dedupes across sources and applies the family filter. kept counts how
// many survived the filter before dedupe, so the caller can tell "filtered out"
// apart from "duplicate".
func collect(results []result, wantFamily string) (addrs []string, kept int) {
	seen := map[string]struct{}{}
	for _, r := range results {
		for _, p := range r.items {
			if wantFamily != "" && !strings.EqualFold(freproxies.DetectFamily(p.Host), wantFamily) {
				continue
			}
			kept++
			// JoinHostPort brackets IPv6, matching what the store records.
			addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
			if _, dup := seen[addr]; dup {
				continue
			}
			seen[addr] = struct{}{}
			addrs = append(addrs, addr)
		}
	}
	sort.Strings(addrs)
	return addrs, kept
}

func writeAddrs(path string, addrs []string) error {
	w := os.Stdout
	if path != "" {
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	for _, a := range addrs {
		if _, err := fmt.Fprintln(w, a); err != nil {
			return err
		}
	}
	return nil
}

func report(results []result, unique, kept int, stats bool) {
	var okCount, errCount, silent int
	var silentNames, errNames []string

	for _, r := range results {
		switch {
		case r.err != nil:
			errCount++
			errNames = append(errNames, fmt.Sprintf("%s (%v)", r.name, r.err))
		case len(r.items) == 0:
			// Fetched fine, parsed nothing: a changed response format or a
			// parser that cannot read this source at all.
			silent++
			silentNames = append(silentNames, r.name)
		default:
			okCount++
		}
	}

	if stats {
		byName := append([]result(nil), results...)
		sort.Slice(byName, func(i, j int) bool { return len(byName[i].items) > len(byName[j].items) })
		fmt.Fprintln(os.Stderr, "\nper-source counts:")
		for _, r := range byName {
			note := ""
			if r.err != nil {
				note = "  error: " + r.err.Error()
			}
			fmt.Fprintf(os.Stderr, "  %-26s %-8s %-12s %6d%s\n", r.name, r.proto, r.format, len(r.items), note)
		}
	}

	fmt.Fprintf(os.Stderr, "\n%d sources: %d ok, %d silent, %d failed\n", len(results), okCount, silent, errCount)
	fmt.Fprintf(os.Stderr, "%d proxies kept, %d unique after dedupe\n", kept, unique)

	if silent > 0 {
		sort.Strings(silentNames)
		fmt.Fprintf(os.Stderr, "\nSILENT (fetched a body, parsed nothing — check the format):\n  %s\n",
			strings.Join(silentNames, "\n  "))
	}
	if errCount > 0 {
		sort.Strings(errNames)
		fmt.Fprintf(os.Stderr, "\nFAILED:\n  %s\n", strings.Join(errNames, "\n  "))
	}
}
