package chanpolicy

import (
	"testing"
	"time"
)

func TestCustomStatusRuleBansOnUnlistedCode(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	// Built-in list is 403/429. 503 should not ban unless a custom rule says so.
	if b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", Status: 503}); b != nil {
		t.Fatal("503 banned by the default policy")
	}
	if _, err := r.AddRule(Rule{Name: "ban 503", Kind: RuleStatus, Statuses: []int{503}, Channel: "ch"}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", Status: 503})
	if b == nil {
		t.Fatal("custom 503 rule did not fire")
	}
	if b.Reason != "rule_status_503" {
		t.Errorf("reason = %q", b.Reason)
	}
}

func TestCustomRuleIsScopedToItsChannel(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	if _, err := r.AddRule(Rule{Name: "tb 503", Kind: RuleStatus, Statuses: []int{503}, Channel: "taobao.com"}); err != nil {
		t.Fatal(err)
	}
	if b := r.Record(Outcome{Channel: "amazon.com", Addr: "1.1.1.1:80", Status: 503}); b != nil {
		t.Fatal("taobao-only rule fired on amazon.com")
	}
	if b := r.Record(Outcome{Channel: "taobao.com", Addr: "1.1.1.1:80", Status: 503}); b == nil {
		t.Fatal("taobao-only rule did not fire on taobao.com")
	}
}

func TestCustomErrorRuleMatchesTag(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) {
		p.ConsecutiveFails = 0
		p.FailRate = 0
		p.TimeoutFails = 0
	})
	if _, err := r.AddRule(Rule{Name: "captcha", Kind: RuleError, Match: "captcha"}); err != nil {
		t.Fatal(err)
	}
	if b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", Err: "dial_failed"}); b != nil {
		t.Fatal("unrelated error fired the captcha rule")
	}
	b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", Err: "captcha_shown"})
	if b == nil || b.Reason != "rule_error" {
		t.Fatalf("captcha rule miss: %+v", b)
	}
}

func TestDeleteRuleStopsFiring(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	rule, err := r.AddRule(Rule{Name: "503", Kind: RuleStatus, Statuses: []int{503}})
	if err != nil {
		t.Fatal(err)
	}
	if !r.DeleteRule(rule.ID) {
		t.Fatal("DeleteRule reported miss")
	}
	if b := r.Record(Outcome{Channel: "ch", Addr: "2.2.2.2:80", Status: 503}); b != nil {
		t.Fatal("deleted rule still fired")
	}
}

func TestNormalizeRuleRejectsEmptyStatusList(t *testing.T) {
	if _, err := normalizeRule(Rule{Kind: RuleStatus}); err == nil {
		t.Fatal("empty status list was accepted")
	}
}

// Rule.TTLSec was stored by the API but never applied: every ban used the
// global ladder instead.
func TestCustomRuleTTLIsApplied(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	if _, err := r.AddRule(Rule{Name: "ban 503 for an hour", Kind: RuleStatus, Statuses: []int{503}, Channel: "ch", TTLSec: 3600}); err != nil {
		t.Fatal(err)
	}
	b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", Status: 503})
	if b == nil {
		t.Fatal("custom rule did not fire")
	}
	got := b.Until.Sub(b.BannedAt)
	if got < 50*time.Minute {
		t.Fatalf("ban lasts %v, want ~1h from the rule's TTLSec rather than the global ladder", got)
	}
}

// Two rules created back to back must not share an ID (AddRule replaces by ID).
func TestRuleIDsAreUnique(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	seen := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		rule, err := r.AddRule(Rule{Name: "r", Kind: RuleStatus, Statuses: []int{503}})
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[rule.ID]; dup {
			t.Fatalf("duplicate rule id %q", rule.ID)
		}
		seen[rule.ID] = struct{}{}
	}
	if len(r.Rules()) != 50 {
		t.Fatalf("registry holds %d rules, want 50", len(r.Rules()))
	}
}
