package directproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"unified-proxy-pool/internal/freproxies"
)

// tunnelThrough uses an already-connected proxy hop to open a tunnel to nextAddr.
// protocol is the protocol of the hop we are currently speaking to.
func tunnelThrough(conn net.Conn, hop freproxies.Proxy, nextAddr string) (net.Conn, error) {
	proto := strings.ToLower(strings.TrimSpace(hop.Protocol))
	switch proto {
	case "socks4", "socks4a":
		// SOCKS4 is a different handshake; speaking SOCKS5 to it fails on the
		// greeting, so these hops could never carry traffic.
		return socks4ConnectOver(conn, nextAddr)
	case "socks5", "socks":
		if _, ok := conn.(*socksAuthed); !ok {
			if err := socks5Handshake(conn, hop.Username, hop.Password); err != nil {
				conn.Close()
				return nil, err
			}
		}
		return socks5ConnectCmd(conn, nextAddr)
	default:
		return httpConnectOver(conn, nextAddr, hop.Username, hop.Password)
	}
}

// socks4ConnectOver issues a SOCKS4/4a CONNECT on an already-open hop.
func socks4ConnectOver(conn net.Conn, target string) (net.Conn, error) {
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		conn.Close()
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		conn.Close()
		return nil, fmt.Errorf("socks4: bad port in %q", target)
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		conn.Close()
		return nil, fmt.Errorf("socks4: IPv6 target not supported")
	}
	req := []byte{0x04, 0x01, byte(port >> 8), byte(port)}
	var hostname string
	if ip := net.ParseIP(host); ip != nil {
		req = append(req, ip.To4()...)
	} else {
		req = append(req, 0, 0, 0, 1) // SOCKS4a: hostname follows
		hostname = host
	}
	req = append(req, 0)
	if hostname != "" {
		req = append(req, hostname...)
		req = append(req, 0)
	}
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}
	resp := make([]byte, 8)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, err
	}
	if resp[0] != 0x00 || resp[1] != 0x5a {
		conn.Close()
		return nil, fmt.Errorf("socks4 connect status %#x", resp[1])
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func httpConnectOver(conn net.Conn, target, user, pass string) (net.Conn, error) {
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
	if !validHostname(hostOnly(target)) {
		conn.Close()
		return nil, fmt.Errorf("invalid CONNECT target")
	}
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n", target, target)
	if user != "" {
		token := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		req += "Proxy-Authorization: Basic " + token + "\r\n"
	}
	req += "\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("chain CONNECT %s status %d", target, resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &prefixConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

func socks5Handshake(conn net.Conn, user, pass string) error {
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if user != "" {
		if _, err := conn.Write([]byte{0x05, 0x02, 0x00, 0x02}); err != nil {
			return err
		}
	} else if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}
	if resp[0] != 0x05 {
		return fmt.Errorf("chain socks5 auth rejected")
	}
	switch resp[1] {
	case 0x00:
	case 0x02:
		ulen := byte(len(user))
		plen := byte(len(pass))
		auth := []byte{0x01, ulen}
		auth = append(auth, []byte(user)...)
		auth = append(auth, plen)
		auth = append(auth, []byte(pass)...)
		if _, err := conn.Write(auth); err != nil {
			return err
		}
		ar := make([]byte, 2)
		if _, err := io.ReadFull(conn, ar); err != nil || ar[1] != 0x00 {
			return fmt.Errorf("chain socks5 user/pass rejected")
		}
	default:
		return fmt.Errorf("chain socks5 auth rejected")
	}
	_ = conn.SetDeadline(time.Time{})
	return nil
}

func socks5ConnectCmd(conn net.Conn, target string) (net.Conn, error) {
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		conn.Close()
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		conn.Close()
		return nil, err
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		conn.Close()
		return nil, err
	}
	if hdr[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("chain socks5 connect failed code=%d", hdr[1])
	}
	switch hdr[3] {
	case 0x01:
		_, err = io.ReadFull(conn, make([]byte, 4+2))
	case 0x03:
		l := make([]byte, 1)
		if _, err = io.ReadFull(conn, l); err == nil {
			_, err = io.ReadFull(conn, make([]byte, int(l[0])+2))
		}
	case 0x04:
		_, err = io.ReadFull(conn, make([]byte, 16+2))
	default:
		err = fmt.Errorf("bad atyp %d", hdr[3])
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// dialProxyChain connects: client path TCP→hop0→hop1→...→target
// hops must be non-empty; length 1 is single-hop.

// chainDialError names the hop that actually broke.
//
// Without it the caller cannot tell a failure at hop 2 from a failure at hop 0,
// and dialChainWithFailover blamed hops[0] for every one of them: the entry
// proxy lost score and was eventually deleted while the hop that really failed
// kept ScoreMax and stayed in rotation to break the next chain too.
type chainDialError struct {
	Hop freproxies.Proxy
	msg string
	Err error
}

func (e *chainDialError) Error() string { return e.msg + ": " + e.Err.Error() }
func (e *chainDialError) Unwrap() error { return e.Err }

// culpritHop reports which hop caused err, when err came from chain dialling.
func culpritHop(err error) (freproxies.Proxy, bool) {
	var ce *chainDialError
	if errors.As(err, &ce) {
		return ce.Hop, true
	}
	return freproxies.Proxy{}, false
}

func dialProxyChainPool(ctx context.Context, hops []freproxies.Proxy, target string, pool *viaPool) (net.Conn, error) {
	if len(hops) == 0 {
		return nil, fmt.Errorf("empty chain")
	}
	conn, err := dialEntry(ctx, hops[0], pool)
	if err != nil {
		return nil, &chainDialError{Hop: hops[0], Err: err,
			msg: fmt.Sprintf("dial entry %s", hops[0].Addr)}
	}

	// Through hop i, reach hop i+1
	for i := 0; i < len(hops)-1; i++ {
		next := hops[i+1].Addr
		conn, err = tunnelThrough(conn, hops[i], next)
		if err != nil {
			return nil, &chainDialError{Hop: hops[i], Err: err,
				msg: fmt.Sprintf("chain hop %d (%s -> %s)", i, hops[i].Addr, next)}
		}
	}
	// Through last hop, reach final target
	last := hops[len(hops)-1]
	conn, err = tunnelThrough(conn, last, target)
	if err != nil {
		return nil, &chainDialError{Hop: last, Err: err,
			msg: fmt.Sprintf("chain exit %s -> %s", last.Addr, target)}
	}
	return conn, nil
}

func uniqueHops(candidates []freproxies.Proxy, n int) []freproxies.Proxy {
	if n <= 0 {
		n = 1
	}
	seen := map[string]struct{}{}
	out := make([]freproxies.Proxy, 0, n)
	for _, p := range candidates {
		if p.Addr == "" {
			continue
		}
		if _, ok := seen[p.Addr]; ok {
			continue
		}
		seen[p.Addr] = struct{}{}
		out = append(out, p)
		if len(out) >= n {
			break
		}
	}
	return out
}

// hostOnly strips the port from a "host:port" target for validation.
func hostOnly(target string) string {
	if h, _, err := net.SplitHostPort(target); err == nil {
		return h
	}
	return target
}
