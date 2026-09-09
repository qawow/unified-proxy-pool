package freproxies

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeProxy serves as an HTTP proxy that answers requests itself instead of
// forwarding them. answers maps a substring of the requested URL to the status
// it replies with; anything unmatched gets a 502, which is how the real
// fabricating proxies behave (they time out, but 502 exercises the same path
// without making the test wait).
func fakeProxy(t *testing.T, answers map[string]int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.RequestURI
		for frag, status := range answers {
			if strings.Contains(target, frag) {
				w.WriteHeader(status)
				return
			}
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// A proxy that only knows how to answer the validate URL must not be recorded
// as alive: it is not forwarding anything.
func TestCheckProxyRejectsFabricatedValidateResponse(t *testing.T) {
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer canary.Close()
	old := canaryURL
	canaryURL = canary.URL
	defer func() { canaryURL = old }()

	addr := fakeProxy(t, map[string]int{"judge.example": http.StatusOK})
	p := Proxy{Addr: addr, Protocol: "http"}

	if _, ok := CheckProxy(context.Background(), p, "http://judge.example/ip", 5*time.Second); ok {
		t.Fatal("a proxy that only answers the validate URL was accepted as alive")
	}
}

// The mirror image: a proxy that reaches both the validate URL and the canary is
// genuinely forwarding and must still pass.
func TestCheckProxyAcceptsProxyThatReachesCanary(t *testing.T) {
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer canary.Close()
	old := canaryURL
	canaryURL = canary.URL
	defer func() { canaryURL = old }()

	addr := fakeProxy(t, map[string]int{
		"judge.example": http.StatusOK,
		strings.TrimPrefix(canary.URL, "http://"): http.StatusNoContent,
	})
	p := Proxy{Addr: addr, Protocol: "http"}

	if _, ok := CheckProxy(context.Background(), p, "http://judge.example/ip", 5*time.Second); !ok {
		t.Fatal("a proxy that reached both targets was rejected")
	}
}

// An HTTPS validate URL is its own proof (certificates are verified), so no
// second round trip is spent on the canary — which is what these two predicates
// decide between.
func TestValidateURLSchemeClassification(t *testing.T) {
	if isPlaintextURL("https://www.gstatic.com/generate_204") {
		t.Error("https URL classified as plaintext")
	}
	if !isPlaintextURL("http://httpbin.org/ip") {
		t.Error("http URL classified as verified")
	}
	if !tlsVerifiedFor("https://x/y") || tlsVerifiedFor("http://x/y") {
		t.Error("tlsVerifiedFor disagrees with isPlaintextURL")
	}
	// A URL that will not parse must fall back to the conservative side.
	if !isPlaintextURL("://bad") {
		t.Error("unparseable URL should be treated as plaintext")
	}
}
