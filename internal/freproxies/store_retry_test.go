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

func TestUntestedFailDeletesImmediately(t *testing.T) {
	ctx := context.Background()
	bothStores(t, func(t *testing.T, s Store) {
		addr := normalizeAddr("10.2.0.1", 9)
		if _, err := s.AddRaw(ctx, []Proxy{{Host: "10.2.0.1", Port: 9, Protocol: "http"}}); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}
		if err := s.MarkValidated(ctx, addr, 0, false); err != nil {
			t.Fatalf("fail: %v", err)
		}
		if _, err := s.Get(ctx, addr); err == nil {
			t.Fatal("untested fail must delete, not park in retry")
		}
		if _, _, err := s.PickRetry(ctx, 10); err != nil {
			t.Fatalf("PickRetry: %v", err)
		}
	})
}

func TestLiveFailRetriesOnceThenDeletes(t *testing.T) {
	ctx := context.Background()
	bothStores(t, func(t *testing.T, s Store) {
		addr := normalizeAddr("10.3.0.1", 9)
		if _, err := s.AddRaw(ctx, []Proxy{{Host: "10.3.0.1", Port: 9, Protocol: "http"}}); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}
		if err := s.MarkValidated(ctx, addr, 20, true); err != nil {
			t.Fatalf("ok: %v", err)
		}
		if err := s.MarkValidated(ctx, addr, 0, false); err != nil {
			t.Fatalf("fail 1: %v", err)
		}
		if _, err := s.Get(ctx, addr); err != nil {
			t.Fatalf("first fail of a live proxy should retry, got %v", err)
		}
		if err := s.MarkValidated(ctx, addr, 0, false); err != nil {
			t.Fatalf("fail 2: %v", err)
		}
		if _, err := s.Get(ctx, addr); err == nil {
			t.Fatal("second fail of a live proxy must delete")
		}
	})
}

func TestRandomNDoesNotServeRaw(t *testing.T) {
	ctx := context.Background()
	bothStores(t, func(t *testing.T, s Store) {
		if _, err := s.AddRaw(ctx, []Proxy{{Host: "10.4.0.1", Port: 1, Protocol: "http"}}); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}
		if _, err := s.RandomN(ctx, "", 1); err == nil {
			t.Fatal("unvalidated raw must not be handed to callers")
		}
	})
}

func TestPurgeRetryClearsZombies(t *testing.T) {
	ctx := context.Background()
	bothStores(t, func(t *testing.T, s Store) {
		addr := normalizeAddr("10.5.0.1", 1)
		if _, err := s.AddRaw(ctx, []Proxy{{Host: "10.5.0.1", Port: 1, Protocol: "http"}}); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}
		if err := s.MarkValidated(ctx, addr, 10, true); err != nil {
			t.Fatalf("ok: %v", err)
		}
		if err := s.MarkValidated(ctx, addr, 0, false); err != nil {
			t.Fatalf("fail: %v", err)
		}
		n, err := s.PurgeRetry(ctx)
		if err != nil {
			t.Fatalf("PurgeRetry: %v", err)
		}
		if n < 1 {
			t.Fatalf("purged %d, want >=1", n)
		}
		if _, err := s.Get(ctx, addr); err == nil {
			t.Fatal("retry zombie still present")
		}
	})
}
