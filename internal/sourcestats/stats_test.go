package sourcestats

import (
	"testing"
	"time"
)

func newClock(start time.Time) (*Registry, *time.Time) {
	now := start.UTC()
	r := New()
	r.now = func() time.Time { return now }
	return r, &now
}

func TestEvaluateDoesNotRefreshTTLWhileDisabled(t *testing.T) {
	r, now := newClock(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	for i := 0; i < 20; i++ {
		r.Record("bad", false, 0)
	}
	r.Evaluate(5, 0.15)
	st, _ := r.Snapshot("bad")
	if !st.AutoDisabled {
		t.Fatal("expected auto-disable")
	}
	firstUntil := st.DisabledUntil
	*now = now.Add(10 * time.Minute)
	r.Evaluate(5, 0.15)
	st, _ = r.Snapshot("bad")
	if !st.DisabledUntil.Equal(firstUntil) {
		t.Fatalf("TTL refreshed from %s to %s", firstUntil, st.DisabledUntil)
	}
	if !r.IsDisabled("bad") {
		t.Fatal("still inside TTL, must stay disabled for pick")
	}
}

func TestRecentRecoveryUnblocksDespiteLifetimeFails(t *testing.T) {
	r, now := newClock(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	for i := 0; i < 40; i++ {
		r.Record("src", false, 0)
	}
	r.Evaluate(5, 0.15)
	if !r.IsDisabled("src") {
		t.Fatal("should disable on a dead recent window")
	}
	*now = now.Add(2 * time.Hour)
	for i := 0; i < 40; i++ {
		r.Record("src", true, 50)
	}
	r.Evaluate(5, 0.15)
	st, _ := r.Snapshot("src")
	if st.SuccessRate > 0.5 {
		t.Fatalf("lifetime rate = %v, test setup should stay poor", st.SuccessRate)
	}
	if r.IsDisabled("src") || st.AutoDisabled {
		t.Fatalf("recent window recovered but still disabled: %+v", st)
	}
}

func TestThinWindowDoesNotDisable(t *testing.T) {
	r, _ := newClock(time.Now())
	r.Record("new", false, 0)
	r.Record("new", false, 0)
	r.Evaluate(5, 0.15)
	if r.IsDisabled("new") {
		t.Fatal("2 samples must not disable")
	}
}

func TestBackoffGrowsAfterExpiredRetryStillBad(t *testing.T) {
	r, now := newClock(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	for i := 0; i < 20; i++ {
		r.Record("bad", false, 0)
	}
	r.Evaluate(5, 0.15)
	st, _ := r.Snapshot("bad")
	first := st.DisabledUntil.Sub(*now)
	*now = st.DisabledUntil.Add(time.Second)
	r.Evaluate(5, 0.15)
	st, _ = r.Snapshot("bad")
	second := st.DisabledUntil.Sub(*now)
	if second <= first {
		t.Fatalf("backoff did not grow: first=%s second=%s", first, second)
	}
}

func TestReenableClearsPenalty(t *testing.T) {
	r, _ := newClock(time.Now())
	for i := 0; i < 20; i++ {
		r.Record("x", false, 0)
	}
	r.Evaluate(5, 0.15)
	r.Reenable("x")
	if r.IsDisabled("x") {
		t.Fatal("Reenable left the source disabled")
	}
}

func TestExpiredDisableIsTrialUntilReEvaluate(t *testing.T) {
	r, now := newClock(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	for i := 0; i < 20; i++ {
		r.Record("x", false, 0)
	}
	r.Evaluate(5, 0.15)
	st, _ := r.Snapshot("x")
	*now = st.DisabledUntil.Add(time.Second)
	if r.IsDisabled("x") {
		t.Fatal("after TTL the source must be usable so a trial sample can land")
	}
}
