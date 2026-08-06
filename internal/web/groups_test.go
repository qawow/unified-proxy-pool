package web

import (
	"testing"

	"unified-proxy-pool/internal/freproxies"
)

func TestNormalizeFamilyParam(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ipv4", freproxies.FamilyIPv4},
		{"IPv4", freproxies.FamilyIPv4},
		{"4", freproxies.FamilyIPv4},
		{"v4", freproxies.FamilyIPv4},
		{"inet", freproxies.FamilyIPv4},
		{" ipv6 ", freproxies.FamilyIPv6},
		{"6", freproxies.FamilyIPv6},
		{"V6", freproxies.FamilyIPv6},
		{"inet6", freproxies.FamilyIPv6},
		{"unknown", freproxies.FamilyUnknown},
		{"hostname", freproxies.FamilyUnknown},
		// Unrecognized input yields "" so the filter is ignored rather than
		// matching nothing and returning an empty list.
		{"", ""},
		{"ipv7", ""},
		{"garbage", ""},
	}
	for _, c := range cases {
		if got := normalizeFamilyParam(c.in); got != c.want {
			t.Errorf("normalizeFamilyParam(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGroupPayloadRule(t *testing.T) {
	p := groupPayload{
		Name:      "g",
		Sources:   []string{"alpha"},
		Protocols: []string{"http"},
		Families:  []string{freproxies.FamilyIPv6},
		Regions:   []string{"US"},
		MinScore:  25,
		OnlyOK:    true,
	}
	r := p.rule()
	if len(r.Sources) != 1 || r.Sources[0] != "alpha" {
		t.Errorf("sources not mapped: %v", r.Sources)
	}
	if len(r.Families) != 1 || r.Families[0] != freproxies.FamilyIPv6 {
		t.Errorf("families not mapped: %v", r.Families)
	}
	if r.MinScore != 25 || !r.OnlyOK {
		t.Errorf("scalar fields not mapped: %+v", r)
	}
}
