package freproxies

import (
	"context"
	"testing"
)

func TestFailLeavesRawSoOtherSourcesCanFill(t *testing.T) {
	ctx := context.Background()
	bothStores(t, func(t *testing.T, s Store) {
		a := normalizeAddr("10.0.0.1", 1)
		if _, err := s.AddRaw(ctx, []Proxy{{Host: "10.0.0.1", Port: 1, Protocol: "http", Source: "src-a"}}); err != nil {
			t.Fatalf("AddRaw a: %v", err)
		}
		if err := s.MarkValidated(ctx, a, 0, false); err != nil {
			t.Fatalf("fail once: %v", err)
		}
		raw, err := s.ListRaw(ctx, 10)
		if err != nil {
			t.Fatalf("ListRaw: %v", err)
		}
		for _, p := range raw {
			if p.Addr == a {
				t.Fatal("failed proxy must leave the untested raw queue")
			}
		}
		if _, err := s.AddRaw(ctx, []Proxy{{Host: "10.0.0.2", Port: 2, Protocol: "http", Source: "src-b"}}); err != nil {
			t.Fatalf("AddRaw b: %v", err)
		}
		raw, err = s.ListRaw(ctx, 10)
		if err != nil {
			t.Fatalf("ListRaw after b: %v", err)
		}
		foundB := false
		for _, p := range raw {
			if p.Source == "src-b" {
				foundB = true
			}
		}
		if !foundB {
			t.Fatal("other source should occupy the freed raw slot")
		}
	})
}

func TestSuccessGoesToMaintenanceList(t *testing.T) {
	ctx := context.Background()
	bothStores(t, func(t *testing.T, s Store) {
		addr := normalizeAddr("10.1.0.1", 8080)
		if _, err := s.AddRaw(ctx, []Proxy{{Host: "10.1.0.1", Port: 8080, Protocol: "http"}}); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}
		if err := s.MarkValidated(ctx, addr, 40, true); err != nil {
			t.Fatalf("ok: %v", err)
		}
		raw, _ := s.ListRaw(ctx, 10)
		for _, p := range raw {
			if p.Addr == addr {
				t.Fatal("live proxy must leave raw")
			}
		}
		got, err := s.ListValidated(ctx, 10)
		if err != nil {
			t.Fatalf("ListValidated: %v", err)
		}
		found := false
		for _, p := range got {
			if p.Addr == addr && p.Validated {
				found = true
			}
		}
		if !found {
			t.Fatal("live proxy must enter the scored maintenance list")
		}
	})
}

func TestThreeFailsDeletes(t *testing.T) {
	ctx := context.Background()
	bothStores(t, func(t *testing.T, s Store) {
		addr := normalizeAddr("10.2.0.1", 9)
		if _, err := s.AddRaw(ctx, []Proxy{{Host: "10.2.0.1", Port: 9, Protocol: "http"}}); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}
		for i := 0; i < failDeleteAfter; i++ {
			if err := s.MarkValidated(ctx, addr, 0, false); err != nil {
				t.Fatalf("fail #%d: %v", i+1, err)
			}
		}
		if _, err := s.Get(ctx, addr); err == nil {
			t.Fatal("expected deletion after 3 failures")
		}
	})
}
