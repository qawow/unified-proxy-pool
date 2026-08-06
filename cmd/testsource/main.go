// Command testsource parses a saved response body with the real dynamic-source
// parser, so a source can be trialled before it is registered.
//
// It reads its configuration from the environment rather than flags because
// scripts/add-source.sh already holds those values as shell variables, and
// passing a URL or column index through the environment cannot be mangled by
// quoting the way a hand-built command line can.
//
//	BODY_FILE=/tmp/body FORMAT=json PROTOCOL=http go run ./cmd/testsource
//
// Output is one `key=value` line per fact, with `parsed=<n>` first so callers
// can read the count without parsing prose.
package main

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/freproxies"
)

func main() {
	bodyFile := os.Getenv("BODY_FILE")
	if bodyFile == "" {
		fmt.Fprintln(os.Stderr, "BODY_FILE is required")
		os.Exit(2)
	}
	body, err := os.ReadFile(bodyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", bodyFile, err)
		os.Exit(1)
	}

	// NewDynamic applies the same defaults and validation the panel applies when
	// a source is saved, so a spec that fails here would fail there too.
	c, err := crawlers.NewDynamic(crawlers.DynamicSpec{
		Name:     "trial",
		URLs:     []string{"http://trial.invalid"},
		Format:   os.Getenv("FORMAT"),
		Protocol: os.Getenv("PROTOCOL"),
		HostCol:  envInt("HOST_COL", 0),
		PortCol:  envInt("PORT_COL", 1),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid spec: %v\n", err)
		os.Exit(1)
	}

	items, err := c.Parse(body, "http://trial.invalid")
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		os.Exit(1)
	}

	// parsed= goes first: callers read it with a single awk line.
	fmt.Printf("parsed=%d\n", len(items))

	byProto := map[string]int{}
	byFamily := map[string]int{}
	for _, p := range items {
		byProto[p.Protocol]++
		byFamily[freproxies.DetectFamily(p.Host)]++
	}
	fmt.Printf("protocols=%s\n", renderCounts(byProto))
	fmt.Printf("families=%s\n", renderCounts(byFamily))

	// A few samples make a wrong column index obvious at a glance: a mis-picked
	// HTML column typically yields plausible-looking ports on the wrong hosts.
	for i, p := range items {
		if i >= 3 {
			break
		}
		fmt.Printf("sample=%s\n", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
	}
}

func envInt(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
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
