package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"testing"

	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/validator"
)

func TestHealthAPIsExposeRealBatchFailureReasons(t *testing.T) {
	store := freproxies.NewMemoryStore()
	free := freproxies.NewService(store, crawlers.NewRegistry(nil), nil, false)
	if _, err := store.AddRaw(t.Context(), []freproxies.Proxy{{Host: "198.51.100.20", Port: 8080, Protocol: "http", Source: "health-test"}}); err != nil {
		t.Fatal(err)
	}
	free.SetProbeFront(func() *freproxies.ProbeFront {
		return &freproxies.ProbeFront{Hop: freproxies.Proxy{Addr: "192.0.2.1:1080", Protocol: "socks5"}}
	}, func(context.Context, []freproxies.Proxy, string) (net.Conn, error) {
		return nil, context.DeadlineExceeded
	})
	cfg := config.App{FreeValidateURL: "https://example.test/", FreeValidateTimeoutMS: 100, FreeValidateConcurrency: 1}
	v := validator.New(cfg, free)
	v.ValidateBatch(t.Context(), 1)
	if b := v.LastBatch(); b.FailReasons["timeout"] != 1 {
		t.Fatalf("real batch lost timeout: %+v", b)
	}
	a := &App{free: free}
	for _, path := range []string{"/api/pool/health", "/api/overview", "/api/validator/queues"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", path, nil)
			switch path {
			case "/api/pool/health":
				a.handlePoolHealth(rec, req)
			case "/api/overview":
				a.handleOverview(rec, req)
			default:
				a.handleValidatorQueues(rec, req)
			}
			var body struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			data := body.Data
			if path == "/api/overview" {
				if err := json.Unmarshal(data["pool_health"], &data); err != nil {
					t.Fatal(err)
				}
			}
			var reasons map[string]int
			if err := json.Unmarshal(data["last_fail_reasons"], &reasons); err != nil {
				t.Fatalf("missing reasons: %s (%v)", rec.Body, err)
			}
			if reasons["timeout"] != 1 {
				t.Fatalf("API lost actual timeout: %v", reasons)
			}
		})
	}
}
