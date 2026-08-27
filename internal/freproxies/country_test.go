package freproxies

import (
	"context"
	"testing"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/geoip"
)

func TestDropBlockedSkipsCNKeepsHK(t *testing.T) {
	prev := geoip.Active()
	t.Cleanup(func() { geoip.SetFilter(prev) })
	geoip.SetFilter(geoip.DefaultFilter())

	svc := NewService(NewMemoryStore(), crawlers.NewRegistry(nil), nil, false)
	kept, n := svc.dropBlocked([]Proxy{
		{Host: "1.1.1.1", Port: 80, Region: "CN"},
		{Host: "2.2.2.2", Port: 80, Region: "中国"},
		{Host: "3.3.3.3", Port: 80, Region: "HK"},
		{Host: "4.4.4.4", Port: 80, Region: "US"},
		{Host: "5.5.5.5", Port: 80},
	})
	if n != 2 {
		t.Fatalf("blocked=%d, want 2", n)
	}
	if len(kept) != 3 {
		t.Fatalf("kept=%d, want 3 (HK, US, unknown)", len(kept))
	}
}

func TestSubmitRawDropsCN(t *testing.T) {
	prev := geoip.Active()
	t.Cleanup(func() { geoip.SetFilter(prev) })
	geoip.SetFilter(geoip.DefaultFilter())

	svc := NewService(NewMemoryStore(), crawlers.NewRegistry(nil), nil, false)
	res, err := svc.SubmitRaw(context.Background(), []Proxy{
		{Host: "1.2.3.4", Port: 8080, Region: "CN"},
		{Host: "8.8.8.8", Port: 8080, Region: "US"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked != 1 {
		t.Errorf("blocked=%d, want 1", res.Blocked)
	}
	if res.Added != 1 {
		t.Errorf("added=%d, want 1", res.Added)
	}
}

func TestPickSkipsCNRegion(t *testing.T) {
	prev := geoip.Active()
	t.Cleanup(func() { geoip.SetFilter(prev) })
	geoip.SetFilter(geoip.DefaultFilter())

	svc := newPickService(t,
		Proxy{Host: "10.0.0.1", Port: 1, Protocol: "http", Region: "CN", LatencyMS: 10},
		Proxy{Host: "10.0.0.2", Port: 1, Protocol: "http", Region: "US", LatencyMS: 10},
	)
	got, err := svc.Pick(context.Background(), PickOptions{N: 8})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got.Items {
		if geoip.Active().Blocks(p.Region) {
			t.Fatalf("picked blocked region %s (%s)", p.Region, p.Addr)
		}
	}
	if len(got.Items) == 0 {
		t.Fatal("US proxy should still be pickable")
	}
}

func TestPurgeBlockedRemovesCN(t *testing.T) {
	prev := geoip.Active()
	t.Cleanup(func() { geoip.SetFilter(prev) })
	geoip.SetFilter(geoip.DefaultFilter())

	svc := NewService(NewMemoryStore(), crawlers.NewRegistry(nil), nil, false)
	ctx := context.Background()
	if _, err := svc.store.AddRaw(ctx, []Proxy{
		{Host: "1.1.1.1", Port: 80, Region: "CN"},
		{Host: "8.8.8.8", Port: 80, Region: "US"},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := svc.PurgeBlocked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("purged=%d, want 1", n)
	}
	list, err := svc.store.List(ctx, ListFilter{Page: 1, Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Region != "US" {
		t.Fatalf("after purge: %+v", list.Items)
	}
}
