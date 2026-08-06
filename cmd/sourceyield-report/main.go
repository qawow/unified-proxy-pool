// Command sourceyield-report shows historical measurements and trends.
//
//	go run ./cmd/sourceyield-report                  # all sources summary
//	go run ./cmd/sourceyield-report -name rdavydov-socks4
//	go run ./cmd/sourceyield-report -trend-only      # only sources with trend data
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"unified-proxy-pool/internal/freproxies"
)

func main() {
	var (
		sourceName = flag.String("name", "", "show history for this source")
		trendOnly  = flag.Bool("trend-only", false, "only show sources with enough data for trend analysis")
		limit      = flag.Int("limit", 10, "max history entries per source")
		redisAddr  = flag.String("redis", envOr("REDIS_ADDR", "127.0.0.1:6379"), "Redis address")
		redisPass  = flag.String("redis-password", os.Getenv("REDIS_PASSWORD"), "Redis password")
		redisDB    = flag.Int("redis-db", envInt("REDIS_DB", 0), "Redis DB number")
	)
	flag.Parse()

	store, err := freproxies.OpenRedis(*redisAddr, *redisPass, *redisDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open redis: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if *sourceName != "" {
		showSourceHistory(ctx, store, *sourceName, *limit)
		return
	}

	summary, err := store.AllSourceYieldSummary(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch summary: %v\n", err)
		os.Exit(1)
	}
	if len(summary) == 0 {
		fmt.Println("No measurements recorded yet. Run `sourceyield -persist` first.")
		return
	}

	names := make([]string, 0, len(summary))
	for name := range summary {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Printf("%-26s %-8s %8s %9s %8s %-10s %-10s %s\n",
		"SOURCE", "MEASURED", "FETCHED", "ALIVE", "EST/RND", "ACTION", "TREND", "WHY")
	fmt.Println(strings.Repeat("-", 120))

	for _, name := range names {
		rec := summary[name]
		trend, records, _ := store.SourceYieldTrend(ctx, name, 5)
		if *trendOnly && len(records) < 2 {
			continue
		}
		ago := time.Since(rec.MeasureAt).Round(time.Minute)
		aliveStr := fmt.Sprintf("%d/%d", rec.Alive, rec.Sampled)
		fmt.Printf("%-26s %-8s %8d %9s %8d %-10s %-10s %s\n",
			truncate(name, 26),
			formatAgo(ago),
			rec.Fetched,
			aliveStr,
			rec.Estimate,
			rec.Action,
			trend,
			truncate(rec.Why, 30))
	}

	fmt.Printf("\nRecorded measurements for %d source(s). Use -name <source> to see history.\n", len(summary))
}

func showSourceHistory(ctx context.Context, store freproxies.Store, name string, limit int) {
	records, err := store.ListSourceYield(ctx, name, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch history: %v\n", err)
		os.Exit(1)
	}
	if len(records) == 0 {
		fmt.Printf("No measurements for source %q.\n", name)
		return
	}

	fmt.Printf("History for %s (newest first, last %d measurements)\n\n", name, limit)
	fmt.Printf("%-20s %8s %9s %8s %-10s %s\n", "MEASURED", "FETCHED", "ALIVE", "EST/RND", "ACTION", "WHY")
	fmt.Println(strings.Repeat("-", 100))

	for _, rec := range records {
		aliveStr := fmt.Sprintf("%d/%d", rec.Alive, rec.Sampled)
		fmt.Printf("%-20s %8d %9s %8d %-10s %s\n",
			rec.MeasureAt.Format("2006-01-02 15:04:05"),
			rec.Fetched,
			aliveStr,
			rec.Estimate,
			rec.Action,
			rec.Why)
	}

	trend, _, err := store.SourceYieldTrend(ctx, name, 5)
	if err == nil && len(records) >= 2 {
		fmt.Printf("\nTrend (last 5 measurements): %s\n", trend)
		switch trend {
		case "IMPROVING":
			fmt.Println("→ This source is getting better over time.")
		case "DEGRADING":
			fmt.Println("→ This source is getting worse. Consider disabling if it continues.")
		case "STABLE":
			fmt.Println("→ This source is consistent.")
		}
	}
}

func formatAgo(d time.Duration) string {
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
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
