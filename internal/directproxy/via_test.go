package directproxy

import (
	"testing"

	"unified-proxy-pool/internal/freproxies"
)

func TestParseViaProxy(t *testing.T) {
	p, err := ParseViaProxy("socks5://alice:secret@203.0.113.9:1080")
	if err != nil {
		t.Fatal(err)
	}
	if p.Protocol != "socks5" || p.Host != "203.0.113.9" || p.Port != 1080 {
		t.Fatalf("got %+v", p)
	}
	if p.Addr != "203.0.113.9:1080" || p.Username != "alice" || p.Password != "secret" {
		t.Fatalf("addr/user %+v", p)
	}
}

func TestParseViaProxyHTTPDefaultPort(t *testing.T) {
	p, err := ParseViaProxy("http://10.0.0.8")
	if err != nil {
		t.Fatal(err)
	}
	if p.Protocol != "http" || p.Port != 8080 {
		t.Fatalf("got %+v", p)
	}
}

func TestAttachViaExitAndEntry(t *testing.T) {
	pool := []freproxies.Proxy{{Addr: "1.1.1.1:8080", Protocol: "http"}}
	via := freproxies.Proxy{Addr: "9.9.9.9:1080", Protocol: "socks5"}
	exit := attachVia(pool, via, "exit")
	if len(exit) != 2 || exit[1].Addr != "9.9.9.9:1080" {
		t.Fatalf("exit hops = %+v", exit)
	}
	entry := attachVia(pool, via, "entry")
	if len(entry) != 2 || entry[0].Addr != "9.9.9.9:1080" {
		t.Fatalf("entry hops = %+v", entry)
	}
	def := attachVia(pool, via, "")
	if def[0].Addr != "9.9.9.9:1080" {
		t.Fatalf("empty mode should be entry (VPS first), got %+v", def)
	}
}

func TestWithViaEmptyIsNoop(t *testing.T) {
	s := &Server{}
	in := []freproxies.Proxy{{Addr: "1.1.1.1:80"}}
	out, err := s.withVia(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Addr != "1.1.1.1:80" {
		t.Fatalf("%+v", out)
	}
}

// An https:// exit_via must not silently become plaintext http: nothing in this
// codebase wraps the proxy hop in TLS, so the scheme has to be refused.
func TestParseViaProxyRejectsHTTPS(t *testing.T) {
	if _, err := ParseViaProxy("https://user:pass@198.51.100.7:8443"); err == nil {
		t.Fatal("https:// exit_via should be rejected, not rewritten to http")
	}
}

// A configured-but-broken exit_via must fail closed. Dropping the via hop on
// error sends traffic out bare while the panel still shows the VPS front.
func TestWithViaFailsClosedOnBadExitVia(t *testing.T) {
	s := &Server{}
	s.SetChainOptions(ChainOptions{ExitVia: "https://203.0.113.9:8443"})
	in := []freproxies.Proxy{{Addr: "1.1.1.1:80"}}
	out, err := s.withVia(in)
	if err == nil {
		t.Fatalf("https:// exit_via must surface an error, got hops %+v", out)
	}
	if out != nil {
		t.Fatalf("fail-closed must return no hops, got %+v", out)
	}
}
