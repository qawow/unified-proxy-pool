package freproxies

import (
	"fmt"
	"testing"
	"time"
)

func TestSelectValidateRawSkipsRecentlyChecked(t *testing.T) {
	now := time.Now()
	items := make([]Proxy, 0, 200)
	for i := 0; i < 200; i++ {
		p := Proxy{Addr: fmt.Sprintf("10.0.0.%d:1", i), Host: fmt.Sprintf("10.0.0.%d", i), Port: 1}
		if i < 120 {
			p.LastCheck = now
		}
		items = append(items, p)
	}
	got := selectValidateRaw(items, 50, now, 90*time.Second)
	if len(got) != 50 {
		t.Fatalf("len=%d want 50", len(got))
	}
	for _, p := range got {
		if !p.LastCheck.IsZero() {
			t.Fatalf("picked recently-checked %s", p.Addr)
		}
	}
}

func TestSelectValidateRawFallsBackToOldestWhenAllRecent(t *testing.T) {
	now := time.Now()
	items := []Proxy{
		{Addr: "a:1", LastCheck: now.Add(-time.Minute)},
		{Addr: "b:1", LastCheck: now.Add(-2 * time.Minute)},
		{Addr: "c:1", LastCheck: now.Add(-3 * time.Minute)},
	}
	got := selectValidateRaw(items, 2, now, 10*time.Minute)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	if got[0].Addr != "c:1" || got[1].Addr != "b:1" {
		t.Fatalf("want oldest-first fallback, got %s %s", got[0].Addr, got[1].Addr)
	}
}

func TestSelectValidateRawTwoDrawsDiverge(t *testing.T) {
	now := time.Now()
	items := make([]Proxy, 0, 400)
	for i := 0; i < 400; i++ {
		items = append(items, Proxy{Addr: fmt.Sprintf("10.1.%d.%d:1", i/256, i%256)})
	}
	first := selectValidateRaw(items, 40, now, 90*time.Second)
	seen := map[string]struct{}{}
	for _, p := range first {
		seen[p.Addr] = struct{}{}
		for i := range items {
			if items[i].Addr == p.Addr {
				items[i].LastCheck = now
			}
		}
	}
	second := selectValidateRaw(items, 40, now, 90*time.Second)
	overlap := 0
	for _, p := range second {
		if _, ok := seen[p.Addr]; ok {
			overlap++
		}
	}
	if overlap != 0 {
		t.Fatalf("second draw reused %d/%d just-checked addrs", overlap, len(second))
	}
}

func TestSelectValidateRawWalksAll(t *testing.T) {
	now := time.Now()
	const n, batch = 100, 10
	items := make([]Proxy, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, Proxy{Addr: fmt.Sprintf("10.2.0.%d:1", i)})
	}
	seen := map[string]struct{}{}
	for round := 0; round < n/batch; round++ {
		got := selectValidateRaw(items, batch, now.Add(time.Duration(round)*time.Second), 90*time.Second)
		if len(got) != batch {
			t.Fatalf("round %d: len=%d", round, len(got))
		}
		for _, p := range got {
			if _, ok := seen[p.Addr]; ok {
				t.Fatalf("round %d reused %s", round, p.Addr)
			}
			seen[p.Addr] = struct{}{}
			for i := range items {
				if items[i].Addr == p.Addr {
					items[i].LastCheck = now.Add(time.Duration(round) * time.Second)
				}
			}
		}
	}
	if len(seen) != n {
		t.Fatalf("covered %d/%d", len(seen), n)
	}
}

func TestCountUnchecked(t *testing.T) {
	now := time.Now()
	items := []Proxy{
		{Addr: "a:1"},
		{Addr: "b:1", LastCheck: now},
		{Addr: "c:1"},
		{Addr: ""},
	}
	if n := countUnchecked(items); n != 2 {
		t.Fatalf("unchecked=%d want 2", n)
	}
}
