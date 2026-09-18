package directproxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"unified-proxy-pool/internal/freproxies"
)

func TestChainFailurePersistsAfterAttemptCancelled(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	addr := strings.TrimPrefix(proxy.URL, "http://")
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	store, err := freproxies.OpenRedis(mr.Addr(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if _, err := store.AddRaw(ctx, []freproxies.Proxy{{Host: host, Port: port, Protocol: "http", Source: "test"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkValidated(ctx, addr, 100, true); err != nil {
		t.Fatal(err)
	}
	free := freproxies.NewService(store, nil, nil, true)
	s := New(Config{}, free)
	opts := DefaultChainOptions()
	opts.FailoverTries = 1
	s.SetChainOptions(opts)
	conn, _, err := s.dialChainWithFailover(ctx, "example.test:443")
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("502 proxy unexpectedly succeeded")
	}
	p, err := store.Get(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	if p.Validated || p.FailCount != 1 {
		t.Fatalf("Redis lost the failed attempt: %+v", p)
	}
}
