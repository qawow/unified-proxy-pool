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
	out := s.withVia(in)
	if len(out) != 1 || out[0].Addr != "1.1.1.1:80" {
		t.Fatalf("%+v", out)
	}
}
