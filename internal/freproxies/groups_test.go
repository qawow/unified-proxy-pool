package freproxies

import (
	"context"
	"testing"
)

func TestDetectFamily(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		{"1.2.3.4", FamilyIPv4},
		{"127.0.0.1", FamilyIPv4},
		{"::1", FamilyIPv6},
		{"[::1]", FamilyIPv6},
		{"2001:db8::1", FamilyIPv6},
		{"[2001:db8::1]", FamilyIPv6},
		{"example.com", FamilyUnknown},
		{"", FamilyUnknown},
		{"999.1.1.1", FamilyUnknown},
		// IPv4-mapped IPv6 normalizes to v4, matching net.IP semantics.
		{"::ffff:1.2.3.4", FamilyIPv4},
	}
	for _, c := range cases {
		if got := DetectFamily(c.host); got != c.want {
			t.Errorf("DetectFamily(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

func TestProxyFamilyFallback(t *testing.T) {
	// Stored value wins.
	p := Proxy{Host: "1.2.3.4", IPFamily: FamilyIPv6}
	if got := p.Family(); got != FamilyIPv6 {
		t.Errorf("stored IPFamily should win, got %q", got)
	}
	// Legacy record without IPFamily derives from Host.
	legacy := Proxy{Host: "2001:db8::5"}
	if got := legacy.Family(); got != FamilyIPv6 {
		t.Errorf("legacy record should derive ipv6, got %q", got)
	}
}

func TestGroupRuleMatches(t *testing.T) {
	v4 := Proxy{Host: "1.2.3.4", Protocol: "http", Source: "alpha", Region: "US", Score: 50, Validated: true}
	v6 := Proxy{Host: "2001:db8::1", Protocol: "socks5", Source: "beta", Region: "JP", Score: 10}

	tests := []struct {
		name  string
		rule  GroupRule
		p     Proxy
		match bool
	}{
		{"empty rule matches all", GroupRule{}, v4, true},
		{"family ipv4 hit", GroupRule{Families: []string{FamilyIPv4}}, v4, true},
		{"family ipv4 miss", GroupRule{Families: []string{FamilyIPv4}}, v6, false},
		{"family ipv6 hit", GroupRule{Families: []string{FamilyIPv6}}, v6, true},
		{"protocol OR within dimension", GroupRule{Protocols: []string{"socks5", "http"}}, v4, true},
		{"source case-insensitive", GroupRule{Sources: []string{"ALPHA"}}, v4, true},
		{"region substring", GroupRule{Regions: []string{"u"}}, v4, true},
		{"region miss", GroupRule{Regions: []string{"cn"}}, v4, false},
		{"min score gate", GroupRule{MinScore: 40}, v6, false},
		{"only_ok gate", GroupRule{OnlyOK: true}, v6, false},
		{"only_ok pass", GroupRule{OnlyOK: true}, v4, true},
		// Different dimensions are ANDed: family matches but protocol does not.
		{"AND across dimensions", GroupRule{Families: []string{FamilyIPv4}, Protocols: []string{"socks5"}}, v4, false},
	}
	for _, tc := range tests {
		if got := tc.rule.Matches(tc.p); got != tc.match {
			t.Errorf("%s: Matches() = %v, want %v", tc.name, got, tc.match)
		}
	}
}

func TestNormalizeGroupName(t *testing.T) {
	ok := []string{"abc", "A1", "my-group", "my_group", "g0"}
	for _, n := range ok {
		if _, err := NormalizeGroupName(n); err != nil {
			t.Errorf("NormalizeGroupName(%q) unexpected error: %v", n, err)
		}
	}
	bad := []string{"", " ", "-lead", "_lead", "has space", "has/slash", "a:b"}
	for _, n := range bad {
		if _, err := NormalizeGroupName(n); err == nil {
			t.Errorf("NormalizeGroupName(%q) expected error, got nil", n)
		}
	}
	got, err := NormalizeGroupName("  MyGroup  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "mygroup" {
		t.Errorf("name should be trimmed+lowercased, got %q", got)
	}
}

func TestNormalizeGroupRule(t *testing.T) {
	// Dedupe and lowercase protocols/families; keep source casing.
	r, err := NormalizeGroupRule(GroupRule{
		Protocols: []string{"HTTP", "http", " socks5 "},
		Families:  []string{"IPv4", "ipv4"},
		Sources:   []string{"Alpha", "alpha", ""},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Protocols) != 2 {
		t.Errorf("protocols should dedupe to 2, got %v", r.Protocols)
	}
	if len(r.Families) != 1 || r.Families[0] != FamilyIPv4 {
		t.Errorf("families should dedupe to [ipv4], got %v", r.Families)
	}
	if len(r.Sources) != 1 {
		t.Errorf("sources should dedupe to 1, got %v", r.Sources)
	}

	if _, err := NormalizeGroupRule(GroupRule{Families: []string{"ipv7"}}); err == nil {
		t.Error("invalid family should be rejected")
	}
	if _, err := NormalizeGroupRule(GroupRule{MinScore: ScoreMax + 1}); err == nil {
		t.Error("out-of-range min_score should be rejected")
	}
}

func TestGroupRuleIsEmpty(t *testing.T) {
	if !(GroupRule{}).IsEmpty() {
		t.Error("zero rule should be empty")
	}
	if (GroupRule{Families: []string{FamilyIPv4}}).IsEmpty() {
		t.Error("rule with family should not be empty")
	}
	if (GroupRule{OnlyOK: true}).IsEmpty() {
		t.Error("rule with only_ok should not be empty")
	}
}

func TestMemoryStoreGroupCRUDAndBuiltins(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	// Builtins are always present, even with no custom groups.
	groups, err := s.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != len(BuiltinGroups()) {
		t.Fatalf("expected %d builtin groups, got %d", len(BuiltinGroups()), len(groups))
	}
	for _, g := range groups {
		if !g.Builtin {
			t.Errorf("group %q should be builtin", g.Name)
		}
	}

	// GetGroup resolves builtins by name without any stored record.
	if g, err := s.GetGroup(ctx, "ipv6"); err != nil || !g.Builtin {
		t.Fatalf("GetGroup(ipv6) = %+v, err=%v", g, err)
	}

	// Save then read back a custom group.
	custom := ProxyGroup{Name: "fast-v6", Label: "快速 IPv6", Rule: GroupRule{Families: []string{FamilyIPv6}, MinScore: 30}}
	if err := s.SaveGroup(ctx, custom); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}
	got, err := s.GetGroup(ctx, "fast-v6")
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if got.Builtin {
		t.Error("custom group must not be marked builtin")
	}
	if got.Rule.MinScore != 30 {
		t.Errorf("rule not persisted, got %+v", got.Rule)
	}

	groups, _ = s.ListGroups(ctx)
	if len(groups) != len(BuiltinGroups())+1 {
		t.Errorf("expected builtins+1, got %d", len(groups))
	}

	if err := s.DeleteGroup(ctx, "fast-v6"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, err := s.GetGroup(ctx, "fast-v6"); err == nil {
		t.Error("deleted group should not resolve")
	}
}

func TestMemoryStoreSetsIPFamilyOnIngest(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	added, err := s.AddRaw(ctx, []Proxy{
		{Host: "1.2.3.4", Port: 8080},
		{Host: "2001:db8::1", Port: 1080},
		{Host: "example.com", Port: 3128},
	})
	if err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	if added != 3 {
		t.Fatalf("expected 3 added, got %d", added)
	}

	// IPv6 addresses must be stored bracketed so host:port stays unambiguous.
	if _, err := s.Get(ctx, "[2001:db8::1]:1080"); err != nil {
		t.Errorf("IPv6 addr should be bracketed in the key: %v", err)
	}

	want := map[string]string{
		"1.2.3.4:8080":       FamilyIPv4,
		"[2001:db8::1]:1080": FamilyIPv6,
		"example.com:3128":   FamilyUnknown,
	}
	for addr, fam := range want {
		p, err := s.Get(ctx, addr)
		if err != nil {
			t.Errorf("Get(%q): %v", addr, err)
			continue
		}
		if p.IPFamily != fam {
			t.Errorf("Get(%q).IPFamily = %q, want %q", addr, p.IPFamily, fam)
		}
	}
}

func TestMemoryStoreListFamilyFilter(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	if _, err := s.AddRaw(ctx, []Proxy{
		{Host: "1.2.3.4", Port: 1},
		{Host: "5.6.7.8", Port: 2},
		{Host: "2001:db8::1", Port: 3},
	}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}

	v4, err := s.List(ctx, ListFilter{Family: FamilyIPv4})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if v4.Total != 2 {
		t.Errorf("ipv4 filter total = %d, want 2", v4.Total)
	}

	v6, _ := s.List(ctx, ListFilter{Family: FamilyIPv6})
	if v6.Total != 1 {
		t.Errorf("ipv6 filter total = %d, want 1", v6.Total)
	}

	all, _ := s.List(ctx, ListFilter{})
	if all.Total != 3 {
		t.Errorf("unfiltered total = %d, want 3", all.Total)
	}
}

func TestMemoryStoreQueuesFamilyCounts(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	if _, err := s.AddRaw(ctx, []Proxy{
		{Host: "1.2.3.4", Port: 1},
		{Host: "2001:db8::1", Port: 2},
	}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	// Family counts are computed over the validated set.
	if err := s.MarkValidated(ctx, "1.2.3.4:1", 100, true); err != nil {
		t.Fatalf("MarkValidated: %v", err)
	}
	if err := s.MarkValidated(ctx, "[2001:db8::1]:2", 120, true); err != nil {
		t.Fatalf("MarkValidated: %v", err)
	}

	q, err := s.Queues(ctx)
	if err != nil {
		t.Fatalf("Queues: %v", err)
	}
	if q.FamilyCounts[FamilyIPv4] != 1 {
		t.Errorf("ipv4 count = %d, want 1", q.FamilyCounts[FamilyIPv4])
	}
	if q.FamilyCounts[FamilyIPv6] != 1 {
		t.Errorf("ipv6 count = %d, want 1", q.FamilyCounts[FamilyIPv6])
	}
}
