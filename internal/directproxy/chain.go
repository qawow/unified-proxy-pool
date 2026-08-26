package directproxy

import (
	"bufio"
	"context"
	"encoding/base64"
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
	case "socks5", "socks", "socks4":
		return socks5ConnectOver(conn, nextAddr, hop.Username, hop.Password)
	default:
		return httpConnectOver(conn, nextAddr, hop.Username, hop.Password)
	}
}

func httpConnectOver(conn net.Conn, target, user, pass string) (net.Conn, error) {
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
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

func socks5ConnectOver(conn net.Conn, target, user, pass string) (net.Conn, error) {
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
	if user != "" {
		if _, err := conn.Write([]byte{0x05, 0x02, 0x00, 0x02}); err != nil {
			conn.Close()
			return nil, err
		}
	} else if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, err
	}
	if resp[0] != 0x05 {
		conn.Close()
		return nil, fmt.Errorf("chain socks5 auth rejected")
	}
	switch resp[1] {
	case 0x00:
		// no auth
	case 0x02:
		ulen := byte(len(user))
		plen := byte(len(pass))
		auth := []byte{0x01, ulen}
		auth = append(auth, []byte(user)...)
		auth = append(auth, plen)
		auth = append(auth, []byte(pass)...)
		if _, err := conn.Write(auth); err != nil {
			conn.Close()
			return nil, err
		}
		ar := make([]byte, 2)
		if _, err := io.ReadFull(conn, ar); err != nil || ar[1] != 0x00 {
			conn.Close()
			return nil, fmt.Errorf("chain socks5 user/pass rejected")
		}
	default:
		conn.Close()
		return nil, fmt.Errorf("chain socks5 auth rejected")
	}
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
func dialProxyChain(ctx context.Context, hops []freproxies.Proxy, target string) (net.Conn, error) {
	if len(hops) == 0 {
		return nil, fmt.Errorf("empty chain")
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", hops[0].Addr)
	if err != nil {
		return nil, fmt.Errorf("dial entry %s: %w", hops[0].Addr, err)
	}

	// Through hop i, reach hop i+1
	for i := 0; i < len(hops)-1; i++ {
		next := hops[i+1].Addr
		conn, err = tunnelThrough(conn, hops[i], next)
		if err != nil {
			return nil, fmt.Errorf("chain hop %d (%s -> %s): %w", i, hops[i].Addr, next, err)
		}
	}
	// Through last hop, reach final target
	last := hops[len(hops)-1]
	conn, err = tunnelThrough(conn, last, target)
	if err != nil {
		return nil, fmt.Errorf("chain exit %s -> %s: %w", last.Addr, target, err)
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
