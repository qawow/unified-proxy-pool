package freproxies

import (
	"strings"
	"testing"
	"time"
)

// yieldHistory builds newest-first records with the given alive counts.
// aliveSeq[0] is the newest measurement.
func yieldHistory(source string, sampled int, aliveSeq ...int) []SourceYieldRecord {
	base := time.Now().UTC()
	out := make([]SourceYieldRecord, 0, len(aliveSeq))
	for i, alive := range aliveSeq {
		action := "KEEP"
		if alive == 0 {
			action = "DISABLE"
		}
		out = append(out, SourceYieldRecord{
			Source:    source,
			MeasureAt: base.Add(-time.Duration(i) * time.Hour),
			Fetched:   sampled * 10,
			Sampled:   sampled,
			Alive:     alive,
			Estimate:  alive * 10,
			Action:    action,
		})
	}
	return out
}

// find returns the decision for a source.
func find(t *testing.T, decisions []TuneDecision, source string) TuneDecision {
	t.Helper()
	for _, d := range decisions {
		if d.Source == source {
			return d
		}
	}
	t.Fatalf("no decision for %q", source)
	return TuneDecision{}
}

// A source dead across the required streak is the case automation exists for.
func TestPlanDisablesConsistentlyDeadSource(t *testing.T) {
	// Two healthy sources keep the global check and the enabled floor satisfied.
	inputs := []TuneInput{
		{Source: "dead", Enabled: true, Records: yieldHistory("dead", 60, 0, 0, 0)},
		{Source: "ok1", Enabled: true, Records: yieldHistory("ok1", 60, 12, 11, 13)},
		{Source: "ok2", Enabled: true, Records: yieldHistory("ok2", 60, 9, 10, 8)},
		{Source: "ok3", Enabled: true, Records: yieldHistory("ok3", 60, 7, 8, 9)},
	}
	decisions, abort := PlanSourceTuning(inputs, DefaultTuneConfig())
	if abort != "" {
		t.Fatalf("unexpected abort: %s", abort)
	}
	if d := find(t, decisions, "dead"); d.Action != TuneDisable {
		t.Errorf("3 dead runs should disable, got %s (%s)", d.Action, d.Reason)
	}
	for _, name := range []string{"ok1", "ok2", "ok3"} {
		if d := find(t, decisions, name); d.Action != TuneKeep {
			t.Errorf("%s is productive and must be kept, got %s (%s)", name, d.Action, d.Reason)
		}
	}
}

// One bad measurement must not disable a source: that is how a transient network
// fault permanently removes a working source, since re-enabling is manual.
func TestPlanWaitsForStreakBeforeDisabling(t *testing.T) {
	inputs := []TuneInput{
		{Source: "blip", Enabled: true, Records: yieldHistory("blip", 60, 0, 14, 15)},
		{Source: "ok1", Enabled: true, Records: yieldHistory("ok1", 60, 12, 11, 13)},
		{Source: "ok2", Enabled: true, Records: yieldHistory("ok2", 60, 9, 10, 8)},
		{Source: "ok3", Enabled: true, Records: yieldHistory("ok3", 60, 7, 8, 9)},
	}
	decisions, abort := PlanSourceTuning(inputs, DefaultTuneConfig())
	if abort != "" {
		t.Fatalf("unexpected abort: %s", abort)
	}
	d := find(t, decisions, "blip")
	if d.Action != TuneKeep {
		t.Errorf("a single dead run must not disable, got %s", d.Action)
	}
	if !strings.Contains(d.Reason, "needs 3") {
		t.Errorf("the reason should read as a countdown to the threshold, got: %s", d.Reason)
	}
}

// A source without enough history is skipped rather than judged.
func TestPlanSkipsThinHistory(t *testing.T) {
	inputs := []TuneInput{
		{Source: "new", Enabled: true, Records: yieldHistory("new", 60, 0)},
		{Source: "ok1", Enabled: true, Records: yieldHistory("ok1", 60, 12, 11, 13)},
		{Source: "ok2", Enabled: true, Records: yieldHistory("ok2", 60, 9, 10, 8)},
		{Source: "ok3", Enabled: true, Records: yieldHistory("ok3", 60, 7, 8, 9)},
	}
	decisions, _ := PlanSourceTuning(inputs, DefaultTuneConfig())
	d := find(t, decisions, "new")
	if d.Action != TuneSkip {
		t.Errorf("1 measurement must be SKIP, not a verdict, got %s (%s)", d.Action, d.Reason)
	}
}

// Sources do not all fail at once. When they appear to, the fault is local — a
// blocked validation URL or a down network — and acting on it would disable the
// entire pool from one bad run.
func TestPlanAbortsWhenEverythingLooksDead(t *testing.T) {
	inputs := []TuneInput{
		{Source: "a", Enabled: true, Records: yieldHistory("a", 60, 0, 0, 0)},
		{Source: "b", Enabled: true, Records: yieldHistory("b", 60, 0, 0, 0)},
		{Source: "c", Enabled: true, Records: yieldHistory("c", 60, 0, 0, 12)},
		{Source: "d", Enabled: true, Records: yieldHistory("d", 60, 11, 10, 12)},
	}
	decisions, abort := PlanSourceTuning(inputs, DefaultTuneConfig())
	if abort == "" {
		t.Fatal("3 of 4 sources dead at once must abort, not disable three quarters of the pool")
	}
	if decisions != nil {
		t.Errorf("an aborted plan must carry no decisions, got %d", len(decisions))
	}
	if !strings.Contains(abort, "validation URL") {
		t.Errorf("the abort should name the likely local cause, got: %s", abort)
	}
}

// Sources measured too few times must not count toward the abort ratio, or a
// handful of new sources could suppress or trigger it.
func TestPlanAbortIgnoresUnmeasuredSources(t *testing.T) {
	inputs := []TuneInput{
		{Source: "dead", Enabled: true, Records: yieldHistory("dead", 60, 0, 0, 0)},
		{Source: "ok1", Enabled: true, Records: yieldHistory("ok1", 60, 12, 11, 13)},
		{Source: "ok2", Enabled: true, Records: yieldHistory("ok2", 60, 9, 10, 8)},
		{Source: "ok3", Enabled: true, Records: yieldHistory("ok3", 60, 7, 8, 9)},
		// Thin history, all dead — must not push the ratio over the threshold.
		{Source: "thin1", Enabled: true, Records: yieldHistory("thin1", 60, 0)},
		{Source: "thin2", Enabled: true, Records: yieldHistory("thin2", 60, 0)},
		{Source: "thin3", Enabled: true, Records: yieldHistory("thin3", 60, 0)},
	}
	decisions, abort := PlanSourceTuning(inputs, DefaultTuneConfig())
	if abort != "" {
		t.Fatalf("sources with too little history must not trigger the abort: %s", abort)
	}
	if d := find(t, decisions, "dead"); d.Action != TuneDisable {
		t.Errorf("the one conclusively dead source should still be disabled, got %s", d.Action)
	}
}

// Disabling is automated, re-enabling is manual, so the plan must never strip the
// pool down to fewer inputs than the floor allows.
func TestPlanRefusesToStarvePool(t *testing.T) {
	inputs := []TuneInput{
		{Source: "d1", Enabled: true, Records: yieldHistory("d1", 60, 0, 0, 0)},
		{Source: "d2", Enabled: true, Records: yieldHistory("d2", 60, 0, 0, 0)},
		{Source: "ok", Enabled: true, Records: yieldHistory("ok", 60, 12, 11, 13)},
	}
	cfg := DefaultTuneConfig()
	cfg.GlobalFailureRatio = 0 // isolate the floor from the abort check
	cfg.MinEnabledSources = 3

	decisions, abort := PlanSourceTuning(inputs, cfg)
	if abort != "" {
		t.Fatalf("unexpected abort: %s", abort)
	}
	disables := 0
	for _, d := range decisions {
		if d.Action == TuneDisable {
			disables++
		}
	}
	if disables != 0 {
		t.Errorf("3 enabled sources with a floor of 3 permits no disables, got %d", disables)
	}
	var explained bool
	for _, d := range decisions {
		if strings.Contains(d.Reason, "floor") {
			explained = true
		}
	}
	if !explained {
		t.Error("a suppressed disable must say the floor blocked it, or it looks like the rule failed")
	}
}

// A recovered source gets switched back on, which is what makes this a loop
// rather than a one-way ratchet toward fewer sources.
func TestPlanReenablesRecoveredSource(t *testing.T) {
	inputs := []TuneInput{
		{Source: "back", Enabled: false, Records: yieldHistory("back", 60, 14, 13, 0)},
		{Source: "ok1", Enabled: true, Records: yieldHistory("ok1", 60, 12, 11, 13)},
		{Source: "ok2", Enabled: true, Records: yieldHistory("ok2", 60, 9, 10, 8)},
		{Source: "ok3", Enabled: true, Records: yieldHistory("ok3", 60, 7, 8, 9)},
	}
	decisions, abort := PlanSourceTuning(inputs, DefaultTuneConfig())
	if abort != "" {
		t.Fatalf("unexpected abort: %s", abort)
	}
	d := find(t, decisions, "back")
	if d.Action != TuneEnable {
		t.Errorf("a source producing again should be re-enabled, got %s (%s)", d.Action, d.Reason)
	}
}

// An oversized source was disabled for volume, not liveness. Re-enabling it
// refills the raw cap and makes Trim evict other sources' proxies.
func TestPlanKeepsOversizedSourceOff(t *testing.T) {
	recs := yieldHistory("huge", 60, 20, 18, 22)
	for i := range recs {
		recs[i].Action = "OVERSIZED"
		recs[i].Fetched = MaxRawProxies * 30
	}
	inputs := []TuneInput{
		{Source: "huge", Enabled: false, Records: recs},
		{Source: "ok1", Enabled: true, Records: yieldHistory("ok1", 60, 12, 11, 13)},
		{Source: "ok2", Enabled: true, Records: yieldHistory("ok2", 60, 9, 10, 8)},
		{Source: "ok3", Enabled: true, Records: yieldHistory("ok3", 60, 7, 8, 9)},
	}
	decisions, _ := PlanSourceTuning(inputs, DefaultTuneConfig())
	d := find(t, decisions, "huge")
	if d.Action != TuneKeep {
		t.Errorf("an oversized source must stay off despite a healthy rate, got %s (%s)",
			d.Action, d.Reason)
	}
	if !strings.Contains(d.Reason, "raw cap") {
		t.Errorf("the reason must name the cap, got: %s", d.Reason)
	}
}

// A disabled source that is alive but barely producing is not worth a fetch
// every round.
func TestPlanLeavesNegligibleSourceOff(t *testing.T) {
	recs := yieldHistory("tiny", 60, 1, 1, 1)
	for i := range recs {
		recs[i].Estimate = 0
		recs[i].Fetched = 3
	}
	inputs := []TuneInput{
		{Source: "tiny", Enabled: false, Records: recs},
		{Source: "ok1", Enabled: true, Records: yieldHistory("ok1", 60, 12, 11, 13)},
		{Source: "ok2", Enabled: true, Records: yieldHistory("ok2", 60, 9, 10, 8)},
		{Source: "ok3", Enabled: true, Records: yieldHistory("ok3", 60, 7, 8, 9)},
	}
	decisions, _ := PlanSourceTuning(inputs, DefaultTuneConfig())
	if d := find(t, decisions, "tiny"); d.Action != TuneKeep {
		t.Errorf("~0 working per round is not worth enabling, got %s (%s)", d.Action, d.Reason)
	}
}

// Every decision must carry a reason. The plan is printed for an operator to
// approve; an unexplained line is not reviewable.
func TestPlanAlwaysExplainsItself(t *testing.T) {
	inputs := []TuneInput{
		{Source: "dead", Enabled: true, Records: yieldHistory("dead", 60, 0, 0, 0)},
		{Source: "thin", Enabled: true, Records: yieldHistory("thin", 60, 5)},
		{Source: "off", Enabled: false, Records: yieldHistory("off", 60, 9, 8, 9)},
		{Source: "ok1", Enabled: true, Records: yieldHistory("ok1", 60, 12, 11, 13)},
		{Source: "ok2", Enabled: true, Records: yieldHistory("ok2", 60, 9, 10, 8)},
		{Source: "ok3", Enabled: true, Records: yieldHistory("ok3", 60, 7, 8, 9)},
	}
	decisions, _ := PlanSourceTuning(inputs, DefaultTuneConfig())
	if len(decisions) != len(inputs) {
		t.Fatalf("every source needs a decision: got %d for %d inputs", len(decisions), len(inputs))
	}
	for _, d := range decisions {
		if strings.TrimSpace(d.Reason) == "" {
			t.Errorf("%s: %s has no reason", d.Source, d.Action)
		}
	}
}

// An empty history set must not abort or panic.
func TestPlanHandlesNoHistory(t *testing.T) {
	decisions, abort := PlanSourceTuning(nil, DefaultTuneConfig())
	if abort != "" {
		t.Errorf("no history is not a failure, got abort: %s", abort)
	}
	if len(decisions) != 0 {
		t.Errorf("expected no decisions, got %d", len(decisions))
	}
}
