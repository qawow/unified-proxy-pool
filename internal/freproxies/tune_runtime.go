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
	n := 0
	for _, d := range decisions {
		switch d.Action {
		case TuneDisable:
			// Do not flip scrapers off from in-process validate samples: that
			// stops the crawl, so there is no recovery signal. sourcestats
			// already hides dead sources from pick. Log the advice only.
			_ = s.store.PushEvent(ctx, fmt.Sprintf("sourcetune would disable %s: %s", d.Source, d.Reason))
			continue
		case TuneEnable:
			if err := s.store.SetScraperEnabled(ctx, d.Source, true); err != nil {
				return n, "", err
			}
			n++
		default:
			continue
		}
		_ = s.store.PushEvent(ctx, fmt.Sprintf("sourcetune %s %s: %s", d.Action, d.Source, d.Reason))
		s.publish("scrapers.toggled", map[string]any{"name": d.Source, "action": d.Action, "reason": d.Reason})
	}
	return n, "", nil
}
