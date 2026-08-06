package freproxies

import (
	"context"
	"testing"

	"unified-proxy-pool/internal/crawlers"
)

// newTestService builds a Service backed by the in-memory store with no broker.
func newTestService() *Service {
	return NewService(NewMemoryStore(), crawlers.NewRegistry(nil), nil, false)
}

func seedProxies(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.store.AddRaw(ctx, []Proxy{
		{Host: "1.2.3.4", Port: 1, Protocol: "http", Source: "alpha", Region: "US"},
		{Host: "5.6.7.8", Port: 2, Protocol: "socks5", Source: "beta", Region: "JP"},
		{Host: "2001:db8::1", Port: 3, Protocol: "http", Source: "alpha", Region: "US"},
		{Host: "2001:db8::2", Port: 4, Protocol: "socks5", Source: "beta", Region: "DE"},
	}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
}

func TestServiceSaveGroupValidation(t *testing.T) {
	ctx := context.Background()
	s := newTestService()

	// Builtin names are reserved.
	if _, err := s.SaveGroup(ctx, "ipv4", "", GroupRule{Families: []string{FamilyIPv4}}); err == nil {
		t.Error("overwriting a builtin group should be rejected")
	}
	// An all-empty rule would silently match everything.
	if _, err := s.SaveGroup(ctx, "empty", "", GroupRule{}); err == nil {
		t.Error("empty rule should be rejected")
	}
	// Invalid identifiers.
	if _, err := s.SaveGroup(ctx, "bad name", "", GroupRule{OnlyOK: true}); err == nil {
		t.Error("invalid group name should be rejected")
	}
	// Invalid family value.
	if _, err := s.SaveGroup(ctx, "okname", "", GroupRule{Families: []string{"ipv9"}}); err == nil {
		t.Error("invalid family should be rejected")
	}

	// Happy path: label defaults to the name.
	g, err := s.SaveGroup(ctx, "V6-Fast", "", GroupRule{Families: []string{"IPV6"}, MinScore: 20})
	if err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}
	if g.Name != "v6-fast" {
		t.Errorf("name should be normalized to lowercase, got %q", g.Name)
	}
	if g.Label != "v6-fast" {
		t.Errorf("label should default to name, got %q", g.Label)
	}
	if len(g.Rule.Families) != 1 || g.Rule.Families[0] != FamilyIPv6 {
		t.Errorf("family should be normalized, got %v", g.Rule.Families)
	}
	if g.Builtin {
		t.Error("custom group must not be builtin")
	}
}

func TestServiceSaveGroupPreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := newTestService()
	first, err := s.SaveGroup(ctx, "g1", "G1", GroupRule{OnlyOK: true})
	if err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}
	second, err := s.SaveGroup(ctx, "g1", "G1 renamed", GroupRule{OnlyOK: true, MinScore: 5})
	if err != nil {
		t.Fatalf("SaveGroup update: %v", err)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("CreatedAt should be preserved across updates: %v vs %v", first.CreatedAt, second.CreatedAt)
	}
	if second.Label != "G1 renamed" {
		t.Errorf("label should update, got %q", second.Label)
	}
}

func TestServiceDeleteGroupRejectsBuiltin(t *testing.T) {
	ctx := context.Background()
	s := newTestService()
	for _, b := range BuiltinGroups() {
		if err := s.DeleteGroup(ctx, b.Name); err == nil {
			t.Errorf("deleting builtin %q should fail", b.Name)
		}
	}
}

func TestServiceListGroupsCounts(t *testing.T) {
	ctx := context.Background()
	s := newTestService()
	seedProxies(t, s)

	views, err := s.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	byName := map[string]ProxyGroupView{}
	for _, v := range views {
		byName[v.Name] = v
	}
	if got := byName["ipv4"].Total; got != 2 {
		t.Errorf("builtin ipv4 total = %d, want 2", got)
	}
	if got := byName["ipv6"].Total; got != 2 {
		t.Errorf("builtin ipv6 total = %d, want 2", got)
	}
	if got := byName["socks5"].Total; got != 2 {
		t.Errorf("builtin socks5 total = %d, want 2", got)
	}
	// Nothing validated yet.
	if got := byName["validated"].Total; got != 0 {
		t.Errorf("builtin validated total = %d, want 0", got)
	}
}

func TestServiceListProxiesResolvesGroup(t *testing.T) {
	ctx := context.Background()
	s := newTestService()
	seedProxies(t, s)

	if _, err := s.SaveGroup(ctx, "v6only", "IPv6 only", GroupRule{Families: []string{FamilyIPv6}}); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}

	res, err := s.ListProxies(ctx, ListFilter{Group: "v6only"})
	if err != nil {
		t.Fatalf("ListProxies: %v", err)
	}
	if res.Total != 2 {
		t.Fatalf("group filter total = %d, want 2", res.Total)
	}
	for _, p := range res.Items {
		if p.Family() != FamilyIPv6 {
			t.Errorf("group v6only returned %s (%s)", p.Addr, p.Family())
		}
	}

	// Group name lookup is case-insensitive.
	if res, err := s.ListProxies(ctx, ListFilter{Group: "V6Only"}); err != nil || res.Total != 2 {
		t.Errorf("group lookup should be case-insensitive: total=%d err=%v", res.Total, err)
	}

	// Unknown groups must error rather than silently returning everything.
	if _, err := s.ListProxies(ctx, ListFilter{Group: "nope"}); err == nil {
		t.Error("unknown group should return an error")
	}
}

func TestServiceListProxiesGroupAndFamilyCombine(t *testing.T) {
	ctx := context.Background()
	s := newTestService()
	seedProxies(t, s)

	// A group scoped to source=alpha, further narrowed by an ipv4 family filter.
	if _, err := s.SaveGroup(ctx, "alpha", "Alpha source", GroupRule{Sources: []string{"alpha"}}); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}
	res, err := s.ListProxies(ctx, ListFilter{Group: "alpha", Family: FamilyIPv4})
	if err != nil {
		t.Fatalf("ListProxies: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("group+family total = %d, want 1", res.Total)
	}
	if res.Items[0].Host != "1.2.3.4" {
		t.Errorf("unexpected item %+v", res.Items[0])
	}
}

func TestServicePickValidatedNFamily(t *testing.T) {
	ctx := context.Background()
	s := newTestService()
	seedProxies(t, s)

	items, err := s.PickValidatedNFamily(ctx, "", FamilyIPv6, 2)
	if err != nil {
		t.Fatalf("PickValidatedNFamily: %v", err)
	}
	for _, p := range items {
		if p.Family() != FamilyIPv6 {
			t.Errorf("expected ipv6 pick, got %s (%s)", p.Addr, p.Family())
		}
	}
}

func TestServicePickFamilyNoMatchErrors(t *testing.T) {
	ctx := context.Background()
	s := newTestService()
	// IPv4 only pool.
	if _, err := s.store.AddRaw(ctx, []Proxy{{Host: "1.2.3.4", Port: 1}}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	if _, err := s.PickValidatedNFamily(ctx, "", FamilyIPv6, 1); err == nil {
		t.Error("requesting ipv6 from an ipv4-only pool should error, not fall back to ipv4")
	}
}
