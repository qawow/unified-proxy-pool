package cfscan

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"
)

func TestTLSProbeHitsLocalCFTrace(t *testing.T) {
	ln := serveTLSTrace(t, "fl=99f\ncolo=LAX\nsliver=a\n")
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	var p int
	fmt.Sscanf(port, "%d", &p)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, ok := tlsCFProbe(ctx, "127.0.0.1", p, "speed.cloudflare.com", 2*time.Second, 2*time.Second)
	if !ok {
		t.Fatal("expected hit")
	}
	if h.Colo != "LAX" {
		t.Fatalf("colo=%s", h.Colo)
	}
}

func TestTLSProbeRejectsPlainHTTP(t *testing.T) {
	ln := serveTLSTrace(t, "welcome to nginx\n")
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	var p int
	fmt.Sscanf(port, "%d", &p)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok := tlsCFProbe(ctx, "127.0.0.1", p, "speed.cloudflare.com", 2*time.Second, 2*time.Second); ok {
		t.Fatal("nginx body must not count as CF")
	}
}

func serveTLSTrace(t *testing.T, body string) net.Listener {
	t.Helper()
	cert := selfCert(t)
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				_, _ = c.Read(buf)
				resp := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n" + body
				_, _ = c.Write([]byte(resp))
			}(c)
		}
	}()
	return ln
}

func selfCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "speed.cloudflare.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"speed.cloudflare.com"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
