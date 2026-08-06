// Command sourcetune turns persisted yield history into source enable/disable
// decisions.
//
// cmd/sourceyield measures and records; this decides. Splitting them matters
// because a decision needs several measurements: a source is disabled only after
// it has read dead across consecutive runs, so one bad network night cannot
// remove a working source.
//
//	go run ./cmd/sourcetune                 # dry run: print the plan
//	go run ./cmd/sourcetune -apply          # commit the plan
//	go run ./cmd/sourcetune -redis-db 15    # target a scratch DB
//
// Writing to shared state is opt-in: the default run changes nothing.
//
// Two refusals are deliberate. The plan aborts when most sources look dead at
// once — sources do not fail together, so that pattern means the validation URL
// or the local network broke, and acting on it would disable the whole pool. And
// it will not drop below a floor of enabled sources, because disabling is
// automated while re-enabling is a human step.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/freproxies"
)

func main() {
	var (
		apply      = flag.Bool("apply", false, "commit the plan (default is a dry run)")
		window     = flag.Int("window", 5, "measurements per source to consider")
		minRuns    = flag.Int("min-runs", 3, "measurements needed before a source is judged")
		deadStreak = flag.Int("dead-streak", 3, "consecutive dead measurements before disabling")
		abortRatio = flag.Float64("abort-ratio", 0.5, "abort if at least this fraction of measured sources look dead")
		minEnabled = flag.Int("min-enabled", 3, "never leave fewer than this many sources enabled")
		minEst     = flag.Int("min-estimate", 1, "working proxies per round needed to re-enable a source")
		redisAddr  = flag.String("redis", envOr("REDIS_ADDR", "127.0.0.1:6379"), "Redis address")
		redisPass  = flag.String("redis-password", os.Getenv("REDIS_PASSWORD"), "Redis password")
		redisDB    = flag.Int("redis-db", envInt("REDIS_DB", 0), "Redis DB number")
	)
	flag.Parse()

	if strings.TrimSpace(*redisAddr) == "" {
		fmt.Fprintln(os.Stderr, "-redis is empty; refusing to fall back to localhost:6379, which is the live pool")
		os.Exit(2)
	}
	store, err := freproxies.OpenRedis(*redisAddr, *redisPass, *redisDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open redis %s db %d: %v\n", *redisAddr, *redisDB, err)
		os.Exit(1)
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := freproxies.TuneConfig{
		MinRuns:            *minRuns,
		ConsecutiveDead:    *deadStreak,
		GlobalFailureRatio: *abortRatio,
		MinEnabledSources:  *minEnabled,
		MinEstimate:        *minEst,
	}

	inputs, err := collectInputs(ctx, store, *window)
	if err != nil {
		fmt.Fprintf(os.Stderr, "collect history: %v\n", err)
		os.Exit(1)
	}
	if len(inputs) == 0 {
		fmt.Println("No yield history recorded yet. Run `sourceyield -persist` a few times first;")
		fmt.Printf("a source needs %d measurements before it can be judged.\n", cfg.MinRuns)
		return
	}

	decisions, abort := freproxies.PlanSourceTuning(inputs, cfg)
	if abort != "" {
		fmt.Fprintf(os.Stderr, "refusing to tune: %s\n", abort)
		os.Exit(3)
	}

	printPlan(decisions, inputs)

	acted := 0
	for _, d := range decisions {
		if d.Action == freproxies.TuneEnable || d.Action == freproxies.TuneDisable {
			acted++
		}
	}
	if acted == 0 {
		fmt.Println("\nNothing to change.")
		return
	}
	if !*apply {
		fmt.Printf("\nDRY RUN — %d change(s) not applied. Re-run with -apply to commit.\n", acted)
		return
	}
	if err := applyPlan(ctx, store, decisions); err != nil {
		fmt.Fprintf(os.Stderr, "apply: %v\n", err)
		os.Exit(1)
	}
}

// collectInputs pairs each source's current enabled state with its history.
//
// The registry is the source of truth for which sources exist, not the history
// index: a source removed from the code should not be tuned, and its default
// enabled state is what SetScraperEnabled is relative to.
func collectInputs(ctx context.Context, store freproxies.Store, window int) ([]freproxies.TuneInput, error) {
	out := make([]freproxies.TuneInput, 0, 64)
	for _, c := range crawlers.DefaultSources() {
		records, err := store.ListSourceYield(ctx, c.Name(), window)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Name(), err)
		}
		if len(records) == 0 {
			continue
		}
		enabled, err := store.IsScraperEnabled(ctx, c.Name(), c.DefaultEnabled())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Name(), err)
		}
		out = append(out, freproxies.TuneInput{
			Source:  c.Name(),
			Enabled: enabled,
			Records: records,
		})
	}
	return out, nil
}

func printPlan(decisions []freproxies.TuneDecision, inputs []freproxies.TuneInput) {
	trends := make(map[string]string, len(inputs))
	for _, in := range inputs {
		trends[in.Source] = trendOf(in.Records)
	}

	fmt.Printf("%-26s %-8s %-10s %-12s %s\n", "SOURCE", "STATE", "ACTION", "TREND", "REASON")
	fmt.Println(strings.Repeat("-", 118))
	for _, d := range decisions {
		state := "off"
		for _, in := range inputs {
			if in.Source == d.Source && in.Enabled {
				state = "on"
			}
		}
		fmt.Printf("%-26s %-8s %-10s %-12s %s\n",
			truncate(d.Source, 26), state, d.Action, trends[d.Source], d.Reason)
	}
}

// trendOf reuses the same classification the report prints, so the two tools
// never disagree about whether a source is degrading.
func trendOf(records []freproxies.SourceYieldRecord) string {
	return freproxies.ClassifyTrend(records)
}

func applyPlan(ctx context.Context, store freproxies.Store, decisions []freproxies.TuneDecision) error {
	var enabled, disabled int
	for _, d := range decisions {
		switch d.Action {
		case freproxies.TuneEnable:
			if err := store.SetScraperEnabled(ctx, d.Source, true); err != nil {
				return fmt.Errorf("enable %s: %w", d.Source, err)
			}
			enabled++
		case freproxies.TuneDisable:
			if err := store.SetScraperEnabled(ctx, d.Source, false); err != nil {
				return fmt.Errorf("disable %s: %w", d.Source, err)
			}
			disabled++
		default:
			continue
		}
		// The panel's event feed is where an operator looks when the pool changes
		// shape without anyone touching it.
		_ = store.PushEvent(ctx, fmt.Sprintf("sourcetune %sd %s: %s", d.Action, d.Source, d.Reason))
	}
	fmt.Printf("\napplied: %d disabled, %d enabled\n", disabled, enabled)
	// SetScraperEnabled is a set, not a toggle, so re-running this is idempotent —
	// unlike the panel's /toggle endpoint, which flips.
	fmt.Println("These are absolute sets, not toggles: re-running makes no further change.")
	return nil
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
