package freproxies

import (
	"context"
	"fmt"
	"log"
	"time"
)

// RecordValidateYield stores one measurement per source from a validate batch.
// Sampled/Alive here are what the validator actually dialed this round.
func (s *Service) RecordValidateYield(ctx context.Context, counts map[string][2]int) {
	if s == nil || s.store == nil || len(counts) == 0 {
		return
	}
	now := time.Now().UTC()
	for name, pair := range counts {
		ok, fail := pair[0], pair[1]
		sampled := ok + fail
		if name == "" || sampled < 20 {
			continue
		}
		rec := SourceYieldRecord{
			Source:    name,
			MeasureAt: now,
			Fetched:   sampled,
			Sampled:   sampled,
			Alive:     ok,
			Estimate:  ok,
			Action:    "KEEP",
			Why:       "validate-batch",
		}
		if ok == 0 {
			rec.Action = "DISABLE"
		}
		if err := s.store.SaveSourceYield(ctx, rec); err != nil {
			log.Printf("sourceyield save %s: %v", name, err)
		}
	}
}

// TuneFromYield applies PlanSourceTuning to scraper enable flags.
func (s *Service) TuneFromYield(ctx context.Context) (applied int, abort string, err error) {
	if s == nil || s.store == nil || s.registry == nil {
		return 0, "", nil
	}
	var inputs []TuneInput
	for _, c := range s.registry.All() {
		enabled, _ := s.store.IsScraperEnabled(ctx, c.Name(), c.DefaultEnabled())
		recs, lerr := s.store.ListSourceYield(ctx, c.Name(), 20)
		if lerr != nil {
			continue
		}
		inputs = append(inputs, TuneInput{Source: c.Name(), Enabled: enabled, Records: recs})
	}
	decisions, abort := PlanSourceTuning(inputs, DefaultTuneConfig())
	if abort != "" {
		log.Printf("sourcetune abort: %s", abort)
		_ = s.store.PushEvent(ctx, "sourcetune abort: "+abort)
		return 0, abort, nil
	}
	// Neither direction flips a scraper automatically any more.
	//
	// Disabling from in-process validate samples stops the crawl, so there is no
	// recovery signal afterwards; sourcestats already hides dead sources from
	// pick. Enabling was worse: a source the operator turned off still has
	// thousands of entries sitting in raw, which kept producing "KEEP"
	// measurements and switched it back on behind their back. Both are logged as
	// advice instead.
	for _, d := range decisions {
		switch d.Action {
		case TuneDisable, TuneEnable:
			_ = s.store.PushEvent(ctx, fmt.Sprintf("sourcetune would %s %s: %s", d.Action, d.Source, d.Reason))
		}
	}
	return 0, "", nil
}
