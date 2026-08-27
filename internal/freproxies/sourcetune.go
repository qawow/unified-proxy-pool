package freproxies

import (
	"fmt"
	"sort"
)

// Tune actions.
const (
	TuneDisable = "disable"
	TuneEnable  = "enable"
	TuneKeep    = "keep"
	TuneSkip    = "skip"
)

// TuneConfig bounds how aggressively persisted history may change source state.
type TuneConfig struct {
	// MinRuns is how many measurements a source needs before it is judged.
	MinRuns int
	// ConsecutiveDead is how many measurements in a row must read DISABLE
	// before a source is turned off. One bad night is not evidence.
	ConsecutiveDead int
	// GlobalFailureRatio aborts the whole plan when at least this fraction of
	// sources look dead at once.
	GlobalFailureRatio float64
	// MinEnabledSources is a floor: tuning will not empty the pool's inputs.
	MinEnabledSources int
	// MinEstimate is the per-round working-proxy count a disabled source must
	// clear before it is re-enabled.
	MinEstimate int
}

func DefaultTuneConfig() TuneConfig {
	return TuneConfig{
		MinRuns:            3,
		ConsecutiveDead:    3,
		GlobalFailureRatio: 0.5,
		MinEnabledSources:  3,
		MinEstimate:        1,
	}
}

// TuneInput is one source's current state plus its history, newest first.
type TuneInput struct {
	Source  string
	Enabled bool
	Records []SourceYieldRecord
}

// TuneDecision is what should happen to one source, and why.
type TuneDecision struct {
	Source string
	Action string
	Reason string
}

// looksDead reports whether a measurement found nothing usable.
// Samples below 20 are treated as inconclusive so a 1-proxy validate batch
// cannot be mixed with CLI 60-sample history and disable a source.
func looksDead(rec SourceYieldRecord) bool {
	if rec.Sampled < 20 {
		return false
	}
	return rec.Action == "DISABLE" || rec.Alive == 0
}

// leadingDead counts how many of the newest measurements in a row read dead.
func leadingDead(records []SourceYieldRecord) int {
	n := 0
	for _, r := range records {
		if !looksDead(r) {
			break
		}
		n++
	}
	return n
}

// PlanSourceTuning turns persisted history into enable/disable decisions.
//
// It returns an abort reason instead of decisions when the evidence points at a
// local fault rather than at the sources. Every free proxy source appearing to
// die within the same round almost always means the validation URL is blocked or
// the network is down; acting on that would disable the entire pool from one bad
// run, and re-enabling is manual. Refusing to act is the recoverable choice.
func PlanSourceTuning(inputs []TuneInput, cfg TuneConfig) (decisions []TuneDecision, abort string) {
	if cfg.MinRuns <= 0 {
		cfg = DefaultTuneConfig()
	}

	// Only sources with enough history count toward the global check; a source
	// measured once should not dilute or trigger it.
	evaluable, deadNow := 0, 0
	for _, in := range inputs {
		if len(in.Records) < cfg.MinRuns {
			continue
		}
		evaluable++
		if looksDead(in.Records[0]) {
			deadNow++
		}
	}
	if evaluable > 0 && cfg.GlobalFailureRatio > 0 {
		ratio := float64(deadNow) / float64(evaluable)
		if ratio >= cfg.GlobalFailureRatio {
			return nil, fmt.Sprintf(
				"%d of %d measured sources look dead (%.0f%%), at or above the %.0f%% abort "+
					"threshold. Sources do not fail together; this points at the validation URL "+
					"or the local network. Nothing was changed — re-measure before tuning.",
				deadNow, evaluable, ratio*100, cfg.GlobalFailureRatio*100)
		}
	}

	for _, in := range inputs {
		decisions = append(decisions, decideOne(in, cfg))
	}
	// Stable, readable order: acted-on first, then by name.
	sort.SliceStable(decisions, func(i, j int) bool {
		rank := func(a string) int {
			switch a {
			case TuneDisable:
				return 0
			case TuneEnable:
				return 1
			case TuneKeep:
				return 2
			default:
				return 3
			}
		}
		if ri, rj := rank(decisions[i].Action), rank(decisions[j].Action); ri != rj {
			return ri < rj
		}
		return decisions[i].Source < decisions[j].Source
	})

	applyEnabledFloor(decisions, inputs, cfg)
	return decisions, ""
}

// decideOne judges a single source from its own history.
func decideOne(in TuneInput, cfg TuneConfig) TuneDecision {
	if len(in.Records) < cfg.MinRuns {
		return TuneDecision{in.Source, TuneSkip, fmt.Sprintf(
			"only %d measurement(s), need %d before judging",
			len(in.Records), cfg.MinRuns)}
	}
	newest := in.Records[0]
	dead := leadingDead(in.Records)

	if in.Enabled {
		if dead >= cfg.ConsecutiveDead {
			return TuneDecision{in.Source, TuneDisable, fmt.Sprintf(
				"%d consecutive measurements found nothing alive (newest %d/%d)",
				dead, newest.Alive, newest.Sampled)}
		}
		if dead > 0 {
			// Degrading but not yet conclusive. Naming the remaining runs makes the
			// report readable as a countdown instead of a silent non-decision.
			return TuneDecision{in.Source, TuneKeep, fmt.Sprintf(
				"%d dead measurement(s) in a row, needs %d to disable",
				dead, cfg.ConsecutiveDead)}
		}
		return TuneDecision{in.Source, TuneKeep, fmt.Sprintf(
			"newest run %d/%d alive, ~%d working per round",
			newest.Alive, newest.Sampled, newest.Estimate)}
	}

	// Disabled source: re-enable only on current, non-trivial output. A source
	// switched off for being oversized must stay off — its problem is volume, not
	// liveness, and re-enabling it would refill the raw cap and evict others.
	if newest.Action == "OVERSIZED" {
		return TuneDecision{in.Source, TuneKeep, fmt.Sprintf(
			"stays off: %d addresses per round overruns the raw cap (%d)",
			newest.Fetched, MaxRawProxies)}
	}
	if looksDead(newest) {
		return TuneDecision{in.Source, TuneKeep, fmt.Sprintf(
			"stays off: newest run still %d/%d alive", newest.Alive, newest.Sampled)}
	}
	if newest.Estimate < cfg.MinEstimate {
		return TuneDecision{in.Source, TuneKeep, fmt.Sprintf(
			"stays off: ~%d working per round is below the %d needed to be worth a fetch",
			newest.Estimate, cfg.MinEstimate)}
	}
	return TuneDecision{in.Source, TuneEnable, fmt.Sprintf(
		"recovered: %d/%d alive, ~%d working per round",
		newest.Alive, newest.Sampled, newest.Estimate)}
}

// applyEnabledFloor downgrades disables that would starve the pool of inputs.
//
// Disabling is automated but re-enabling is a human step, so the failure mode is
// asymmetric: too many disables in one run leaves the pool with no sources and
// nobody watching. The floor counts sources this plan cannot see as still
// enabled, so it is a floor on the whole configuration rather than on the sample.
func applyEnabledFloor(decisions []TuneDecision, inputs []TuneInput, cfg TuneConfig) {
	if cfg.MinEnabledSources <= 0 {
		return
	}
	enabled := 0
	for _, in := range inputs {
		if in.Enabled {
			enabled++
		}
	}
	byName := make(map[string]TuneInput, len(inputs))
	for _, in := range inputs {
		byName[in.Source] = in
	}
	// Later entries are the weaker cases: decisions are ordered disables-first,
	// and within that by name, so walking forward keeps the clearest disables.
	projected := enabled
	for i := range decisions {
		if decisions[i].Action != TuneDisable {
			continue
		}
		if projected-1 >= cfg.MinEnabledSources {
			projected--
			continue
		}
		newest := SourceYieldRecord{}
		if in, ok := byName[decisions[i].Source]; ok && len(in.Records) > 0 {
			newest = in.Records[0]
		}
		decisions[i] = TuneDecision{decisions[i].Source, TuneKeep, fmt.Sprintf(
			"would be disabled (%d/%d alive) but that would leave %d enabled source(s), "+
				"below the floor of %d; disable it by hand if that is what you want",
			newest.Alive, newest.Sampled, projected-1, cfg.MinEnabledSources)}
	}
}
