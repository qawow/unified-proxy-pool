// Command checkproxies validates a list of proxies using the pool's own
// liveness check, so its verdict matches what the panel would record.
//
//	go run ./cmd/checkproxies -in raw.txt -out live.txt
//	go run ./cmd/fetchproxies | go run ./cmd/checkproxies -concurrency 200
//	go run ./cmd/checkproxies -in raw.txt -proto socks5 -url https://www.gstatic.com/generate_204
//
// Input is one address per line ("1.2.3.4:8080" or "[2001:db8::1]:1080");
// blank lines and #-comments are skipped. Working proxies go to -out (stdout by
// default), one per line, fastest first. The summary goes to stderr so the two
// can be piped apart.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"unified-proxy-pool/internal/freproxies"
)

type checked struct {
	addr      string
	family    string
	latencyMS int64
}

func main() {
	var (
		in          = flag.String("in", "", "input file (default stdin)")
		out         = flag.String("out", "", "output file (default stdout)")
		proto       = flag.String("proto", "http", "how to speak to these proxies: http, socks4, socks5")
		validateURL = flag.String("url", "https://www.gstatic.com/generate_204", "URL to fetch through each proxy")
		timeout     = flag.Duration("timeout", 8*time.Second, "per-proxy timeout")
		concurrency = flag.Int("concurrency", 100, "parallel checks")
		family      = flag.String("family", "", "only check this IP family (ipv4, ipv6)")
		showLatency = flag.Bool("latency", false, "append \\t<ms> to each line")
		quiet       = flag.Bool("quiet", false, "suppress the progress counter")
	)
	flag.Parse()

	addrs, err := readAddrs(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}
	if *family != "" {
		addrs = filterFamily(addrs, *family)
	}
	if len(addrs) == 0 {
		fmt.Fprintln(os.Stderr, "no addresses to check")
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr, "checking %d proxies as %s (concurrency %d, timeout %s)\n",
		len(addrs), *proto, *concurrency, *timeout)

	live := check(addrs, *proto, *validateURL, *timeout, *concurrency, *quiet)

	// Fastest first: the caller usually wants the head of this list.
	sort.Slice(live, func(i, j int) bool { return live[i].latencyMS < live[j].latencyMS })

	if err := writeResults(*out, live, *showLatency); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	summarize(len(addrs), live)
}

func readAddrs(path string) ([]string, error) {
	f := os.Stdin
	if path != "" {
		var err error
		f, err = os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 1024)
	sc := bufio.NewScanner(f)
	// Long lines are possible when a source packs extra columns onto each row.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Tolerate "addr<tab>anything" and "http://addr" shapes.
		line = strings.Fields(line)[0]
		if i := strings.Index(line, "://"); i >= 0 {
			line = line[i+3:]
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	return out, sc.Err()
}

func filterFamily(addrs []string, want string) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		host, _, ok := splitAddrHost(a)
		if !ok {
			continue
		}
		if strings.EqualFold(freproxies.DetectFamily(host), want) {
			out = append(out, a)
		}
	}
	return out
}

// splitAddrHost pulls the host out of "host:port" / "[v6]:port" without
// validating the port, which CheckProxy will do anyway.
func splitAddrHost(addr string) (string, string, bool) {
	if i := strings.LastIndex(addr, ":"); i > 0 {
		host := addr[:i]
		host = strings.TrimPrefix(host, "[")
		host = strings.TrimSuffix(host, "]")
		return host, addr[i+1:], true
	}
	return "", "", false
}

func check(addrs []string, proto, validateURL string, timeout time.Duration, concurrency int, quiet bool) []checked {
	if concurrency < 1 {
		concurrency = 1
	}
	var (
		mu   sync.Mutex
		live []checked
		done int64
	)
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, addr := range addrs {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			host, _, _ := splitAddrHost(addr)
			// A generous per-proxy ceiling: CheckProxy already applies its own
			// timeout, this only stops a pathological hang.
			ctx, cancel := context.WithTimeout(context.Background(), timeout+2*time.Second)
			defer cancel()

			ms, ok := freproxies.CheckProxy(ctx, freproxies.Proxy{
				Addr: addr, Host: host, Protocol: proto,
			}, validateURL, timeout)

			if n := atomic.AddInt64(&done, 1); !quiet && n%200 == 0 {
				fmt.Fprintf(os.Stderr, "  %d/%d checked\n", n, len(addrs))
			}
			if !ok {
				return
			}
			mu.Lock()
			live = append(live, checked{addr: addr, family: freproxies.DetectFamily(host), latencyMS: ms})
			mu.Unlock()
		}(addr)
	}
	wg.Wait()
	return live
}

func writeResults(path string, live []checked, showLatency bool) error {
	w := os.Stdout
	if path != "" {
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	for _, c := range live {
		if showLatency {
			if _, err := fmt.Fprintf(bw, "%s\t%d\n", c.addr, c.latencyMS); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintln(bw, c.addr); err != nil {
			return err
		}
	}
	return nil
}

func summarize(total int, live []checked) {
	byFamily := map[string]int{}
	var sum int64
	for _, c := range live {
		byFamily[c.family]++
		sum += c.latencyMS
	}
	rate := 0.0
	if total > 0 {
		rate = float64(len(live)) / float64(total) * 100
	}
	fmt.Fprintf(os.Stderr, "\n%d/%d alive (%.1f%%)\n", len(live), total, rate)
	if len(live) > 0 {
		fmt.Fprintf(os.Stderr, "median %dms, fastest %dms, mean %dms\n",
			live[len(live)/2].latencyMS, live[0].latencyMS, sum/int64(len(live)))
		fmt.Fprintf(os.Stderr, "by family: ")
		families := make([]string, 0, len(byFamily))
		for f := range byFamily {
			families = append(families, f)
		}
		sort.Strings(families)
		for _, f := range families {
			fmt.Fprintf(os.Stderr, "%s=%d ", f, byFamily[f])
		}
		fmt.Fprintln(os.Stderr)
	}
}
