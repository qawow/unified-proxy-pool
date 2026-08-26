package directproxy

import (
	"errors"
	"fmt"
	"testing"

	"unified-proxy-pool/internal/chanpolicy"
	"unified-proxy-pool/internal/freproxies"
)

// recordingPolicy captures what the server files, and can pretend anything is
// banned.
type recordingPolicy struct {
	channel   string
	outcomes  []chanpolicy.Outcome
	bannedSet map[string]bool
}

func newRecordingPolicy(channel string) *recordingPolicy {
	return &recordingPolicy{channel: channel, bannedSet: map[string]bool{}}
}

func (r *recordingPolicy) ChannelFor(string) string { return r.channel }

func (r *recordingPolicy) Record(o chanpolicy.Outcome) *chanpolicy.Ban {
	r.outcomes = append(r.outcomes, o)
	return nil
}

func (r *recordingPolicy) Banned(_, addr string) bool { return r.bannedSet[addr] }

// errTag has to preserve the timeout/refused distinction because the two trip
// different ban rules; everything else can collapse.
func TestErrTagClassification(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{errors.New("dial tcp 1.2.3.4:80: i/o timeout"), "timeout"},
		{errors.New("context deadline exceeded"), "timeout"},
		{errors.New("dial tcp 1.2.3.4:80: connect: connection refused"), "conn_refused"},
		{errors.New("read: connection reset by peer"), "conn_reset"},
		{errors.New("lookup nope.invalid: no such host"), "dns_failed"},
		{errors.New("upstream CONNECT status 407"), "upstream_rejected"},
		{errors.New("something else entirely"), "dial_failed"},
	}
	for _, c := range cases {
		if got := errTag(c.err); got != c.want {
			t.Errorf("errTag(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestChannelForReturnsEmptyWithoutPolicy(t *testing.T) {
	s := &Server{}
	if got := s.channelFor("item.taobao.com:443"); got != "" {
		t.Errorf("channelFor without a policy = %q, want empty", got)
	}
}

// recordChannel must be inert when there is nothing to attribute to, so callers
// do not have to guard every call site.
func TestRecordChannelIgnoresEmptyInputs(t *testing.T) {
	pol := newRecordingPolicy("taobao.com")
	s := &Server{}
	s.SetChannelPolicy(pol)

	s.recordChannel("", "1.2.3.4:80", false, 0, "x", 0)
	s.recordChannel("taobao.com", "", false, 0, "x", 0)
	if len(pol.outcomes) != 0 {
		t.Errorf("recorded %d outcomes for empty inputs, want 0", len(pol.outcomes))
	}

	s.recordChannel("taobao.com", "1.2.3.4:80", false, 403, "reported", 12)
	if len(pol.outcomes) != 1 {
		t.Fatalf("recorded %d outcomes, want 1", len(pol.outcomes))
	}
	got := pol.outcomes[0]
	if got.Channel != "taobao.com" || got.Addr != "1.2.3.4:80" || got.Status != 403 || got.OK {
		t.Errorf("outcome = %+v, not the values passed in", got)
	}
}

func hop(addr string) freproxies.Proxy {
	return freproxies.Proxy{Addr: addr, Protocol: "http"}
}

// Only the exit hop is visible to the destination, so only it needs replacing.
func TestAvoidBannedExitSwapsOnlyTheExit(t *testing.T) {
	pol := newRecordingPolicy("taobao.com")
	pol.bannedSet["10.0.0.3:80"] = true
	s := &Server{}
	s.SetChannelPolicy(pol)

	hops := []freproxies.Proxy{hop("10.0.0.1:80"), hop("10.0.0.2:80"), hop("10.0.0.3:80")}
	pool := append(hops, hop("10.0.0.9:80"))

	got := s.avoidBannedExit(hops, pool, "taobao.com")
	if len(got) != 3 {
		t.Fatalf("chain length changed to %d", len(got))
	}
	if got[2].Addr != "10.0.0.9:80" {
		t.Errorf("exit hop = %s, want the clean substitute 10.0.0.9:80", got[2].Addr)
	}
	// The relay hops the destination never sees must be left alone.
	if got[0].Addr != "10.0.0.1:80" || got[1].Addr != "10.0.0.2:80" {
		t.Errorf("relay hops were disturbed: %s, %s", got[0].Addr, got[1].Addr)
	}
}

// A banned relay hop is irrelevant: the destination never sees it.
func TestAvoidBannedExitLeavesBannedRelayHopsAlone(t *testing.T) {
	pol := newRecordingPolicy("taobao.com")
	pol.bannedSet["10.0.0.1:80"] = true
	s := &Server{}
	s.SetChannelPolicy(pol)

	hops := []freproxies.Proxy{hop("10.0.0.1:80"), hop("10.0.0.2:80")}
	got := s.avoidBannedExit(hops, append(hops, hop("10.0.0.9:80")), "taobao.com")
	if got[0].Addr != "10.0.0.1:80" {
		t.Error("replaced a banned relay hop; only the exit is subject to the destination's bans")
	}
}

// With no clean substitute, a chain with a banned exit still beats no chain.
func TestAvoidBannedExitKeepsChainWhenNoCleanCandidate(t *testing.T) {
	pol := newRecordingPolicy("taobao.com")
	pol.bannedSet["10.0.0.1:80"] = true
	pol.bannedSet["10.0.0.2:80"] = true
	s := &Server{}
	s.SetChannelPolicy(pol)

	hops := []freproxies.Proxy{hop("10.0.0.1:80"), hop("10.0.0.2:80")}
	got := s.avoidBannedExit(hops, hops, "taobao.com")
	if len(got) != 2 || got[1].Addr != "10.0.0.2:80" {
		t.Errorf("chain was dropped rather than kept with a banned exit: %+v", got)
	}
}

func TestAvoidBannedExitNoopWithoutChannel(t *testing.T) {
	pol := newRecordingPolicy("")
	pol.bannedSet["10.0.0.2:80"] = true
	s := &Server{}
	s.SetChannelPolicy(pol)

	hops := []freproxies.Proxy{hop("10.0.0.1:80"), hop("10.0.0.2:80")}
	got := s.avoidBannedExit(hops, hops, "")
	if fmt.Sprint(got) != fmt.Sprint(hops) {
		t.Errorf("chain changed with channel tracking off: %+v", got)
	}
}
