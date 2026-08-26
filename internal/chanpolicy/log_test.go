package chanpolicy

import "testing"

func TestLogsCaptureOutcomeThatFiredABan(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	r.Record(Outcome{Channel: "taobao.com", Addr: "1.1.1.1:80", Status: 403})

	logs := r.Logs("", 0)
	if len(logs) != 1 {
		t.Fatalf("got %d log lines, want 1", len(logs))
	}
	e := logs[0]
	if e.Channel != "taobao.com" || e.Addr != "1.1.1.1:80" {
		t.Errorf("logged %s/%s, want taobao.com/1.1.1.1:80", e.Channel, e.Addr)
	}
	if !e.Banned || e.Reason != "status_403" {
		t.Errorf("Banned=%v Reason=%q; the triggering line must be marked so the panel can highlight it",
			e.Banned, e.Reason)
	}
	if e.Status != 403 {
		t.Errorf("Status = %d, want 403", e.Status)
	}
}

func TestLogsKeepNonTriggeringOutcomes(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) {
		p.FailRate = 0
		p.TimeoutFails = 0
	})
	fail(r, "ch", "1.1.1.1:80")
	ok(r, "ch", "1.1.1.1:80")

	logs := r.Logs("ch", 0)
	if len(logs) != 2 {
		t.Fatalf("got %d lines, want both the fail and the success", len(logs))
	}
	if logs[0].OK || logs[0].Banned {
		t.Errorf("first line = %+v, want an unmarked failure", logs[0])
	}
	if !logs[1].OK || logs[1].Banned {
		t.Errorf("second line = %+v, want an unmarked success", logs[1])
	}
}

func TestLogsFilterByChannel(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	fail(r, "taobao.com", "1.1.1.1:80")
	fail(r, "amazon.com", "2.2.2.2:80")

	got := r.Logs("taobao.com", 0)
	if len(got) != 1 || got[0].Channel != "taobao.com" {
		t.Errorf("channel filter returned %+v", got)
	}
}

func TestLogsRespectLimitAndNewestLast(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	for i := 0; i < 5; i++ {
		ok(r, "ch", "1.1.1.1:80")
	}
	got := r.Logs("ch", 2)
	if len(got) != 2 {
		t.Fatalf("limit 2 returned %d lines", len(got))
	}
	// Newest last matches the validator log, so the panel can keep scrolling down.
	if got[0].At.After(got[1].At) {
		t.Error("log is not newest-last")
	}
}

func TestClearLogsDoesNotTouchBans(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", Status: 403})
	if !r.Banned("ch", "1.1.1.1:80") {
		t.Fatal("fixture failed to ban")
	}
	r.ClearLogs()
	if len(r.Logs("", 0)) != 0 {
		t.Error("ClearLogs left entries behind")
	}
	if !r.Banned("ch", "1.1.1.1:80") {
		t.Error("ClearLogs released a ban; the log is forensics, not the source of truth")
	}
}

func TestDisabledPolicyDoesNotLog(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) { p.Enabled = false })
	fail(r, "ch", "1.1.1.1:80")
	if n := len(r.Logs("", 0)); n != 0 {
		t.Errorf("logged %d lines while the policy is disabled", n)
	}
}

func TestLogRingDropsOldestWhenFull(t *testing.T) {
	ring := newLogRing(3)
	for i := 0; i < 5; i++ {
		ring.add(LogEntry{Addr: itoa(i)})
	}
	got := ring.list("", 0)
	if len(got) != 3 {
		t.Fatalf("ring held %d, cap is 3", len(got))
	}
	if got[0].Addr != "2" || got[2].Addr != "4" {
		t.Errorf("kept %+v, want the three newest (2,3,4)", []string{got[0].Addr, got[1].Addr, got[2].Addr})
	}
}
