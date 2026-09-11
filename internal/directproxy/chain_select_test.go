package directproxy

import (
	"testing"

	"unified-proxy-pool/internal/freproxies"
)

// The failover rotation relies on uniqueHops to keep one proxy from occupying
// two positions of the same chain.
func TestUniqueHopsDedupesByAddr(t *testing.T) {
	candidates := []freproxies.Proxy{
		{Addr: "10.0.0.1:80", Protocol: "http"},
		{Addr: "10.0.0.2:80", Protocol: "http"},
		{Addr: "10.0.0.1:80", Protocol: "http"}, // duplicate addr
		{Addr: "10.0.0.3:80", Protocol: "http"},
	}
	got := uniqueHops(candidates, 3)
	if len(got) != 3 {
		t.Fatalf("uniqueHops = %+v, want 3 distinct", got)
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p.Addr] {
			t.Fatalf("addr %s twice in %+v", p.Addr, got)
		}
		seen[p.Addr] = true
	}
}

func TestUniqueHopsSkipsEmptyAddr(t *testing.T) {
	got := uniqueHops([]freproxies.Proxy{{Addr: "", Protocol: "http"}, {Addr: "10.0.0.1:80"}}, 2)
	if len(got) != 1 || got[0].Addr != "10.0.0.1:80" {
		t.Fatalf("uniqueHops = %+v", got)
	}
}

// Entry/exit protocol preferences are soft: the matching proxy is swapped into
// position, but a pool without any match still serves a chain.
func TestApplyEntryExitPrefsSwaps(t *testing.T) {
	hops := []freproxies.Proxy{
		{Addr: "10.0.0.1:80", Protocol: "http"},
		{Addr: "10.0.0.2:1080", Protocol: "socks5"},
		{Addr: "10.0.0.3:80", Protocol: "http"},
	}
	got := applyEntryExitPrefs(hops, ChainOptions{EntryProto: "socks5"})
	if got[0].Protocol != "socks5" {
		t.Fatalf("entry hop = %+v, want the socks5 proxy first", got[0])
	}
	if len(got) != 3 {
		t.Fatalf("chain length changed: %+v", got)
	}

	got = applyEntryExitPrefs(hops, ChainOptions{ExitProto: "socks5"})
	if got[len(got)-1].Protocol != "socks5" {
		t.Fatalf("exit hop = %+v, want the socks5 proxy last", got[len(got)-1])
	}

	// No match anywhere: the chain must come back untouched, not empty.
	got = applyEntryExitPrefs(hops, ChainOptions{EntryProto: "socks4", ExitProto: "socks4"})
	if len(got) != len(hops) {
		t.Fatalf("chain lost hops when no protocol matched: %+v", got)
	}
}
