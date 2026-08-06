// Command sources prints the built-in scraper manifest.
//
// It exists so the shell scripts under scripts/ never carry their own copy of
// the source list: a hand-maintained duplicate drifts silently, and a source
// that has quietly died looks identical to one that was never there.
//
//	go run ./cmd/sources                          # every source, TSV
//	go run ./cmd/sources -enabled                 # only default-enabled
//	go run ./cmd/sources -format plaintext        # only one parser format
//	go run ./cmd/sources -proto socks5 -urls      # bare URLs, for xargs
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"unified-proxy-pool/internal/crawlers"
)

func main() {
	var (
		onlyEnabled = flag.Bool("enabled", false, "only sources enabled by default")
		wantFormat  = flag.String("format", "", "only this parser format (plaintext, json, html_table, regex)")
		wantProto   = flag.String("proto", "", "only this protocol (http, https, socks4, socks5)")
		urlsOnly    = flag.Bool("urls", false, "print bare URLs, one per line")
		tsv         = flag.Bool("tsv", false, "tab-separated instead of aligned columns")
	)
	flag.Parse()

	type row struct{ name, proto, format, enabled, url string }
	rows := make([]row, 0, 256)

	for _, c := range crawlers.DefaultSources() {
		if *onlyEnabled && !c.DefaultEnabled() {
			continue
		}
		format := crawlers.CrawlerFormat(c)
		if *wantFormat != "" && !strings.EqualFold(format, *wantFormat) {
			continue
		}
		if *wantProto != "" && !strings.EqualFold(c.Protocol(), *wantProto) {
			continue
		}
		enabled := "off"
		if c.DefaultEnabled() {
			enabled = "on"
		}
		for _, u := range c.URLs() {
			rows = append(rows, row{c.Name(), c.Protocol(), format, enabled, u})
		}
	}

	if *urlsOnly {
		for _, r := range rows {
			fmt.Println(r.url)
		}
		return
	}
	if *tsv {
		for _, r := range rows {
			fmt.Printf("%s\t%s\t%s\t%s\t%s\n", r.name, r.proto, r.format, r.enabled, r.url)
		}
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROTO\tFORMAT\tDEFAULT\tURL")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.name, r.proto, r.format, r.enabled, r.url)
	}
	_ = w.Flush()
	fmt.Fprintf(os.Stderr, "\n%d url(s)\n", len(rows))
}
