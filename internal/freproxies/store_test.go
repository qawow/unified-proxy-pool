package freproxies

import (
	"context"
	"sort"
	"testing"
	"time"
)

// Every validated proxy scores ScoreMax, so ordering by keyScored degenerated
// to address order: the same prefix was re-checked forever and the rest of the
// pool went stale. Revalidation must follow last-check time instead.
func TestRedisListValidatedOrdersByLastCheck(t *testing.T) {
	store, mr := newFakeRedisStore(t)
	ctx := context.Background()

	// Addresses deliberately ordered opposite to the check order.
	addrs := []string{"9.9.9.9:9090", "5.5.5.5:5050", "1.1.1.1:1010"}
	raw := make([]Proxy, 0, len(addrs))
	for _, a := range addrs {
		host, port, _ := splitAddr(a)
		raw = append(raw, Proxy{Addr: a, Host: host, Port: port, Protocol: "http"})
	}
	if _, err := store.AddRaw(ctx, raw); err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if err := store.MarkValidated(ctx, a, 100, true); err != nil {
			t.Fatal(err)
		}
		// keyChecked has one-second resolution; miniredis lets us move its clock
		// instead of sleeping.
		mr.FastForward(2 * time.Second)
	}

	got, err := store.ListValidated(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d proxies, want 3", len(got))
	}
	if got[0].Addr != addrs[0] {
		t.Fatalf("first revalidation candidate = %s, want the oldest check %s", got[0].Addr, addrs[0])
	}
	if got[2].Addr != addrs[2] {
		t.Fatalf("last candidate = %s, want the newest check %s", got[2].Addr, addrs[2])
	}
}

// RandomN used to read a fixed lexicographic window (ZRevRange 0..127), so the
// lexicographically smallest addresses could never be handed out no matter how
// often it was called.
func TestRedisRandomNReachesWholePool(t *testing.T) {
	store, _ := newFakeRedisStore(t)
	ctx := context.Background()

	const n = 200
	raw := make([]Proxy, 0, n)
	addrs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		host := "10.0." + itoa(i/256) + "." + itoa(i%256)
		raw = append(raw, Proxy{Host: host, Port: 8080, Protocol: "http"})
		addrs = append(addrs, host+":8080")
	}
	if _, err := store.AddRaw(ctx, raw); err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if err := store.MarkValidated(ctx, a, 50, true); err != nil {
			t.Fatal(err)
		}
	}

	// Equal scores make Redis order members lexicographically, so these are the
	// entries a fixed top-N window would starve.
	sort.Strings(addrs)
	starved := map[string]struct{}{}
	for _, a := range addrs[:20] {
		starved[a] = struct{}{}
	}

	for i := 0; i < 60; i++ {
		got, err := store.RandomN(ctx, "", 5)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range got {
			if _, ok := starved[p.Addr]; ok {
				return // reached the part of the pool the old window excluded
			}
		}
	}
	t.Fatal("RandomN never returned any of the 20 lowest-ordered proxies")
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
