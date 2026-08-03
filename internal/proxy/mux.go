package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"unified-proxy-pool/internal/pools"
	"unified-proxy-pool/internal/traffic"
)

const (
	protocolDetectTimeout = 5 * time.Second
	maxHTTPStartLineBytes = 4096
	maxHTTPHeaderBytes    = 32 * 1024
)

// Mux multiplexes a single TCP listener into proxy traffic (SOCKS5 / HTTP proxy)
// and regular HTTP panel traffic, sharing one port.
type Mux struct {
	pools      *pools.Service
	httpServer *http.Server
	listener   net.Listener
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	mu          sync.Mutex
	activeConns map[net.Conn]struct{}
}

func NewMux(poolSvc *pools.Service, httpHandler http.Handler, addr string) (*Mux, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Mux{
		pools:       poolSvc,
		listener:    ln,
		ctx:         ctx,
		cancel:      cancel,
		activeConns: make(map[net.Conn]struct{}),
	}
	// The HTTP server serves connections that are forwarded via peekedListener
	m.httpServer = &http.Server{
		Handler:           httpHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return m, nil
}

func (m *Mux) Addr() net.Addr {
	return m.listener.Addr()
}

// Serve starts accepting connections. Blocks until the listener is closed.
func (m *Mux) Serve() error {
	httpConns := make(chan net.Conn, 64)
	pl := &peekedListener{ch: httpConns, addr: m.listener.Addr(), done: m.ctx.Done()}

	// Serve HTTP in background
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		if err := m.httpServer.Serve(pl); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("proxy mux: http server error: %v", err)
		}
	}()

	for {
		conn, err := m.listener.Accept()
		if err != nil {
			select {
			case <-m.ctx.Done():
				return nil
			default:
			}
			return err
		}
		m.wg.Add(1)
		go func(c net.Conn) {
			defer m.wg.Done()
			m.handleConn(c, httpConns)
		}(conn)
	}
}

func (m *Mux) Shutdown(ctx context.Context) error {
	m.cancel()
	_ = m.listener.Close()

	// Close all tracked active proxy connections so relay goroutines unblock
	m.mu.Lock()
	for c := range m.activeConns {
		_ = c.Close()
	}
	m.mu.Unlock()

	err := m.httpServer.Shutdown(ctx)
	m.wg.Wait()
	return err
}

func (m *Mux) trackConn(c net.Conn) {
	m.mu.Lock()
	m.activeConns[c] = struct{}{}
	m.mu.Unlock()
}

func (m *Mux) untrackConn(c net.Conn) {
	m.mu.Lock()
	delete(m.activeConns, c)
	m.mu.Unlock()
}

func (m *Mux) handleConn(conn net.Conn, httpConns chan<- net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(protocolDetectTimeout))
	br := bufio.NewReaderSize(conn, maxHTTPHeaderBytes)
	firstByte, err := br.Peek(1)
	if err != nil {
		conn.Close()
		return
	}

	pc := &peekedConn{Conn: conn, reader: br}

	if firstByte[0] == 0x05 {
		// SOCKS5
		m.handleSOCKS5(pc)
		return
	}

	// Peek a small amount to check if this is an HTTP proxy request.
	// Only peek what's already buffered + a small amount to avoid blocking.
	line, err := peekRequestLine(br)
	if err != nil {
		conn.Close()
		return
	}

	if isHTTPProxyRequest(line) {
		m.handleHTTPProxy(pc)
		return
	}

	_ = conn.SetReadDeadline(time.Time{})

	// Regular HTTP → pass to HTTP server. Avoid blocking indefinitely if the
	// queue is full or the mux is shutting down.
	select {
	case httpConns <- pc:
	case <-m.ctx.Done():
		_ = pc.Close()
	default:
		_, _ = pc.Write([]byte("HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"))
		_ = pc.Close()
	}
}

func isHTTPProxyRequest(firstLine string) bool {
	// CONNECT host:port HTTP/1.x
	if strings.HasPrefix(firstLine, "CONNECT ") {
		return true
	}
	// GET http://... / POST http://... etc (absolute URL = proxy request)
	methods := []string{"GET ", "POST ", "PUT ", "DELETE ", "HEAD ", "OPTIONS ", "PATCH "}
	for _, method := range methods {
		if strings.HasPrefix(firstLine, method) {
			rest := firstLine[len(method):]
			if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
				return true
			}
		}
	}
	return false
}

func (m *Mux) handleSOCKS5(conn *peekedConn) {
	defer conn.Close()

	// Read SOCKS5 greeting
	// Version + nMethods already in buffer (first byte was 0x05)
	version := make([]byte, 1)
	if _, err := io.ReadFull(conn, version); err != nil || version[0] != 0x05 {
		return
	}
	nMethods := make([]byte, 1)
	if _, err := io.ReadFull(conn, nMethods); err != nil {
		return
	}
	methods := make([]byte, nMethods[0])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}

	// Require username/password auth (method 0x02)
	hasUserPass := false
	for _, method := range methods {
		if method == 0x02 {
			hasUserPass = true
			break
		}
	}
	if !hasUserPass {
		conn.Write([]byte{0x05, 0xFF}) // No acceptable methods
		return
	}
	conn.Write([]byte{0x05, 0x02}) // Select username/password

	// Read username/password subnegotiation (RFC 1929)
	authVer := make([]byte, 1)
	if _, err := io.ReadFull(conn, authVer); err != nil || authVer[0] != 0x01 {
		return
	}
	uLen := make([]byte, 1)
	if _, err := io.ReadFull(conn, uLen); err != nil {
		return
	}
	username := make([]byte, uLen[0])
	if _, err := io.ReadFull(conn, username); err != nil {
		return
	}
	pLen := make([]byte, 1)
	if _, err := io.ReadFull(conn, pLen); err != nil {
		return
	}
	password := make([]byte, pLen[0])
	if _, err := io.ReadFull(conn, password); err != nil {
		return
	}

	pool, err := m.pools.LookupPoolByAuth(m.ctx, string(username), string(password))
	if err != nil || pool == nil {
		conn.Write([]byte{0x01, 0x01}) // Auth failure
		return
	}
	conn.Write([]byte{0x01, 0x00}) // Auth success

	// Connect to internal Mihomo listener and relay the rest
	internalPort, err := pools.InternalPort(pool.ID)
	if err != nil {
		log.Printf("proxy mux: invalid internal port for pool %d: %v", pool.ID, err)
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	internalAddr := fmt.Sprintf("127.0.0.1:%d", internalPort)
	upstream, err := net.DialTimeout("tcp", internalAddr, 5*time.Second)
	if err != nil {
		log.Printf("proxy mux: failed to dial internal %s: %v", internalAddr, err)
		// Send SOCKS5 general failure reply
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	_ = upstream.SetDeadline(time.Now().Add(protocolDetectTimeout))

	// Handshake with internal Mihomo SOCKS5 listener.
	// Offer both no-auth (0x00) and username/password (0x02).
	if _, err := upstream.Write([]byte{0x05, 0x02, 0x00, 0x02}); err != nil {
		log.Printf("proxy mux: failed to write SOCKS5 greeting to internal %s: %v", internalAddr, err)
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	gresp := make([]byte, 2)
	if _, err := io.ReadFull(upstream, gresp); err != nil {
		log.Printf("proxy mux: failed to read SOCKS5 greeting response from internal %s: %v", internalAddr, err)
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	if gresp[0] != 0x05 {
		log.Printf("proxy mux: invalid SOCKS5 version from internal %s: %d", internalAddr, gresp[0])
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	switch gresp[1] {
	case 0x00:
		// No authentication required — proceed
	case 0x02:
		// Perform RFC 1929 username/password auth with internal listener
		authReq := make([]byte, 0, 3+len(username)+len(password))
		authReq = append(authReq, 0x01, byte(len(username)))
		authReq = append(authReq, username...)
		authReq = append(authReq, byte(len(password)))
		authReq = append(authReq, password...)
		if _, err := upstream.Write(authReq); err != nil {
			log.Printf("proxy mux: failed to write RFC1929 auth to internal %s: %v", internalAddr, err)
			conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
		authResp := make([]byte, 2)
		if _, err := io.ReadFull(upstream, authResp); err != nil || authResp[0] != 0x01 || authResp[1] != 0x00 {
			log.Printf("proxy mux: internal SOCKS5 auth failed for %s", internalAddr)
			conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
	default:
		log.Printf("proxy mux: unsupported SOCKS5 auth method from internal %s: %d", internalAddr, gresp[1])
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Track connections for graceful shutdown
	m.trackConn(conn)
	m.trackConn(upstream)
	defer m.untrackConn(conn)
	defer m.untrackConn(upstream)

	_ = conn.SetReadDeadline(time.Time{})
	_ = upstream.SetDeadline(time.Time{})

	// Now relay: client's SOCKS5 request/reply and all subsequent data go straight through
	relay(conn, upstream)
}

func (m *Mux) handleHTTPProxy(conn *peekedConn) {
	defer conn.Close()

	header, err := peekHTTPHeader(conn.reader)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"))
		return
	}

	// Parse Proxy-Authorization header from the full request header.
	username, password := extractProxyAuth(header)
	if username == "" {
		// Send 407 Proxy Authentication Required
		resp := "HTTP/1.1 407 Proxy Authentication Required\r\n" +
			"Proxy-Authenticate: Basic realm=\"proxy\"\r\n" +
			"Content-Length: 0\r\n\r\n"
		conn.Write([]byte(resp))
		return
	}

	pool, err := m.pools.LookupPoolByAuth(m.ctx, username, password)
	if err != nil || pool == nil {
		resp := "HTTP/1.1 407 Proxy Authentication Required\r\n" +
			"Proxy-Authenticate: Basic realm=\"proxy\"\r\n" +
			"Content-Length: 0\r\n\r\n"
		conn.Write([]byte(resp))
		return
	}

	// Dial internal Mihomo HTTP proxy
	internalPort, err := pools.InternalPort(pool.ID)
	if err != nil {
		log.Printf("proxy mux: invalid internal port for pool %d: %v", pool.ID, err)
		resp := "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"
		conn.Write([]byte(resp))
		return
	}
	internalAddr := fmt.Sprintf("127.0.0.1:%d", internalPort)
	upstream, err := net.DialTimeout("tcp", internalAddr, 5*time.Second)
	if err != nil {
		log.Printf("proxy mux: failed to dial internal %s: %v", internalAddr, err)
		resp := "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"
		conn.Write([]byte(resp))
		return
	}
	defer upstream.Close()

	// Track connections for graceful shutdown
	m.trackConn(conn)
	m.trackConn(upstream)
	defer m.untrackConn(conn)
	defer m.untrackConn(upstream)

	_ = conn.SetReadDeadline(time.Time{})

	// Relay everything from buffered conn to upstream (and back)
	relay(conn, upstream)
}

func peekRequestLine(br *bufio.Reader) (string, error) {
	line, err := peekBufferedUntil(br, []byte("\r\n"), maxHTTPStartLineBytes)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(line), "\r\n"), nil
}

func peekHTTPHeader(br *bufio.Reader) (string, error) {
	header, err := peekBufferedUntil(br, []byte("\r\n\r\n"), maxHTTPHeaderBytes)
	if err != nil {
		return "", err
	}
	return string(header), nil
}

func peekBufferedUntil(br *bufio.Reader, delimiter []byte, maxBytes int) ([]byte, error) {
	size := br.Buffered()
	if size < 1 {
		size = 1
	}
	if size > maxBytes {
		size = maxBytes
	}

	for {
		data, err := br.Peek(size)
		if idx := bytes.Index(data, delimiter); idx >= 0 {
			return data[:idx+len(delimiter)], nil
		}
		if err != nil {
			return nil, err
		}
		if size >= maxBytes {
			return nil, fmt.Errorf("request header exceeds %d bytes", maxBytes)
		}
		size = len(data) + 1
		if size > maxBytes {
			size = maxBytes
		}
	}
}

func extractProxyAuth(header string) (string, string) {
	lines := strings.Split(header, "\r\n")
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "proxy-authorization: basic ") {
			encoded := strings.TrimSpace(line[len("proxy-authorization: basic "):])
			// Find end of encoded value (may have trailing content)
			if idx := strings.IndexByte(encoded, '\r'); idx >= 0 {
				encoded = encoded[:idx]
			}
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
			if err != nil {
				return "", ""
			}
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) == 2 {
				return parts[0], parts[1]
			}
		}
	}
	return "", ""
}

func relay(a, b net.Conn) {
	relayMux(a, b, "mux")
}

func relayMux(client, upstream net.Conn, channel string) {
	traffic.Default.BeginInbound(channel)
	traffic.Default.BeginOutbound(channel)
	up, down, err := traffic.BidirectionalRelay(client, upstream)
	ok := err == nil
	traffic.Default.EndConn(channel, ok, up, down, true)
}

// peekedConn wraps a net.Conn with a bufio.Reader so peeked bytes are not lost.
type peekedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

func (c *peekedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// peekedListener feeds pre-accepted connections from a channel into http.Server.Serve.
type peekedListener struct {
	ch   <-chan net.Conn
	addr net.Addr
	done <-chan struct{}
}

func (pl *peekedListener) Accept() (net.Conn, error) {
	select {
	case <-pl.done:
		return nil, net.ErrClosed
	case conn, ok := <-pl.ch:
		if !ok {
			return nil, net.ErrClosed
		}
		return conn, nil
	}
}

func (pl *peekedListener) Close() error   { return nil }
func (pl *peekedListener) Addr() net.Addr { return pl.addr }
