package freproxies

import (
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// recordingConnectProxy accepts a CONNECT, hands the request text to the test,
// and answers 502 — enough to inspect what the transport put on the wire
// without needing a real origin.
func recordingConnectProxy(t *testing.T) (string, chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	reqs := make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 0, 512)
				b := make([]byte, 1)
				for len(buf) < 512 {
					if _, err := c.Read(b); err != nil {
						return
					}
					buf = append(buf, b[0])
					if strings.HasSuffix(string(buf), "\r\n\r\n") {
						break
					}
				}
				reqs <- string(buf)
				_, _ = c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
			}(c)
		}
	}()
	return ln.Addr().String(), reqs
}

// The exit-country probe shares the validation transport, so an authenticated
// HTTP proxy must receive its credentials — the old private transport dropped
// them and the geo lookup failed for every paid/manual proxy.
func TestProbeTransportSendsProxyAuth(t *testing.T) {
	proxyAddr, reqs := recordingConnectProxy(t)
	p := Proxy{Addr: proxyAddr, Protocol: "http", Username: "alice", Password: "s3cret"}
	rt := probeTransport(p, 2*time.Second, true, nil)
	client := &http.Client{Transport: rt, Timeout: 2 * time.Second}
	_, _ = client.Get("https://geo.test/")
	select {
	case req := <-reqs:
		want := "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
		if !strings.Contains(req, want) {
			t.Fatalf("CONNECT lacked credentials: %q", req)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("proxy never saw a CONNECT")
	}
}

// strictSOCKS4 speaks only SOCKS4: it rejects a SOCKS5 greeting outright, so a
// transport that maps socks4 onto socks5 never gets a tunnel.
func strictSOCKS4(t *testing.T) (addr string, firstByte chan byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	firstByte = make(chan byte, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				hdr := make([]byte, 8)
				if _, err := io.ReadFull(c, hdr); err != nil {
					return
				}
				firstByte <- hdr[0]
				if hdr[0] != 0x04 {
					return // strict: SOCKS5 greeting dies here
				}
				// Read the NUL-terminated USERID, then the SOCKS4a hostname.
				for i := 0; i < 2; i++ {
					b := make([]byte, 1)
					for {
						if _, err := c.Read(b); err != nil {
							return
						}
						if b[0] == 0 {
							break
						}
					}
					if i == 0 && !(hdr[4] == 0 && hdr[5] == 0 && hdr[6] == 0) {
						break // SOCKS4: no hostname follows
					}
				}
				// Granted.
				resp := []byte{0x00, 0x5a, hdr[2], hdr[3], hdr[4], hdr[5], hdr[6], hdr[7]}
				if _, err := c.Write(resp); err != nil {
					return
				}
				// Answer the tunneled HTTP request so the probe sees a 2xx.
				buf := make([]byte, 1024)
				_, _ = c.Read(buf)
				_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
			}(c)
		}
	}()
	return ln.Addr().String(), firstByte
}

// A socks4 candidate must be dialled with the SOCKS4 wire protocol — the old
// country transport labelled it socks5 and the strict server above would have
// closed on the 0x05 greeting.
func TestProbeTransportDialsSOCKS4(t *testing.T) {
	proxyAddr, firstByte := strictSOCKS4(t)
	p := Proxy{Addr: proxyAddr, Protocol: "socks4"}
	rt := probeTransport(p, 2*time.Second, false, nil)
	client := &http.Client{Transport: rt, Timeout: 3 * time.Second}
	resp, err := client.Get("http://geo.test/")
	if err != nil {
		t.Fatalf("request through strict SOCKS4 proxy: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	select {
	case b := <-firstByte:
		if b != 0x04 {
			t.Fatalf("proxy got first byte %#x, want SOCKS4 0x04", b)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy saw no request")
	}
}
