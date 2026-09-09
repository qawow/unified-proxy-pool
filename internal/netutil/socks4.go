package netutil

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// DialSOCKS4 opens a TCP connection to target through a SOCKS4/4a proxy.
//
// net/http and golang.org/x/net/proxy speak SOCKS5 only, so socks4 entries used
// to be dialled with a SOCKS5 greeting the peer never understood: every socks4
// proxy failed validation, and the ~15 socks4 lists enabled by default filled
// the raw cap with addresses that could not possibly pass.
//
// SOCKS4a (hostname passed to the proxy) is used when the target is not an IPv4
// literal. SOCKS4 has no IPv6 support at all.
func DialSOCKS4(ctx context.Context, d *net.Dialer, proxyAddr, target string) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("socks4: bad target %q: %w", target, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("socks4: bad port in %q", target)
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return nil, fmt.Errorf("socks4: IPv6 target %q is not supported", host)
	}
	if d == nil {
		d = &net.Dialer{}
	}
	conn, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	req := make([]byte, 0, 16+len(host))
	req = append(req, 0x04, 0x01) // VN=4, CD=1 (CONNECT)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	var hostname string
	if ip := net.ParseIP(host); ip != nil {
		req = append(req, ip.To4()...)
	} else {
		// SOCKS4a marker: an address of 0.0.0.x means "hostname follows".
		req = append(req, 0, 0, 0, 1)
		hostname = host
	}
	req = append(req, 0) // empty USERID, NUL-terminated
	if hostname != "" {
		req = append(req, hostname...)
		req = append(req, 0)
	}
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks4: write request: %w", err)
	}

	resp := make([]byte, 8)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks4: read reply: %w", err)
	}
	if resp[0] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks4: bad reply version %#x", resp[0])
	}
	if resp[1] != 0x5a {
		conn.Close()
		return nil, fmt.Errorf("socks4: request rejected (code %#x)", resp[1])
	}
	// Clear the handshake deadline; the caller owns the connection's timing.
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// IsSOCKS4 reports whether a protocol string names SOCKS4.
func IsSOCKS4(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "socks4", "socks4a":
		return true
	}
	return false
}
