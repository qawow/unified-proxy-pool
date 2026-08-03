package proxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"unified-proxy-pool/internal/auth"
	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/events"
	"unified-proxy-pool/internal/mihomo"
	"unified-proxy-pool/internal/models"
	"unified-proxy-pool/internal/nodes"
	"unified-proxy-pool/internal/pools"
	"unified-proxy-pool/internal/settings"
	"unified-proxy-pool/internal/subscriptions"
)

func TestMuxRoutesPanelHTTP(t *testing.T) {
	mux, serveDone := startTestMux(t, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("panel"))
	}))
	defer stopTestMux(t, mux, serveDone)

	resp, err := http.Get("http://" + mux.Addr().String() + "/health")
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "panel" {
		t.Fatalf("unexpected panel response: status=%d body=%q", resp.StatusCode, string(body))
	}
}

func TestMuxRoutesHTTPProxyWithDelayedProxyAuthorization(t *testing.T) {
	poolSvc, pool := newTestPoolService(t, 101, "demo-user", "demo-pass")
	internalPort, err := pools.InternalPort(pool.ID)
	if err != nil {
		t.Fatalf("InternalPort() error = %v", err)
	}

	requests := make(chan string, 1)
	internalDone := startMockHTTPProxyServer(t, internalPort, requests)

	mux, serveDone := startTestMux(t, poolSvc, http.NewServeMux())
	defer stopTestMux(t, mux, serveDone)

	conn, err := net.Dial("tcp", mux.Addr().String())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	authValue := base64.StdEncoding.EncodeToString([]byte("demo-user:demo-pass"))
	firstChunk := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nUser-Agent: integration-test\r\n"
	secondChunk := "Proxy-Authorization: Basic " + authValue + "\r\n\r\n"

	if _, err := io.WriteString(conn, firstChunk); err != nil {
		t.Fatalf("write first HTTP proxy chunk: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := io.WriteString(conn, secondChunk); err != nil {
		t.Fatalf("write second HTTP proxy chunk: %v", err)
	}

	responseHeader, err := readUntil(conn, "\r\n\r\n")
	if err != nil {
		t.Fatalf("read HTTP proxy response header: %v", err)
	}
	if !strings.Contains(responseHeader, "200 Connection Established") {
		t.Fatalf("expected successful CONNECT response, got %q", responseHeader)
	}

	select {
	case request := <-requests:
		if !strings.Contains(request, "Proxy-Authorization: Basic "+authValue) {
			t.Fatalf("expected forwarded Proxy-Authorization header, got %q", request)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for internal HTTP proxy request")
	}

	if err := <-internalDone; err != nil {
		t.Fatalf("mock HTTP proxy server error = %v", err)
	}
}

func TestMuxRoutesSOCKS5WithAuth(t *testing.T) {
	poolSvc, pool := newTestPoolService(t, 102, "demo-user", "demo-pass")
	internalPort, err := pools.InternalPort(pool.ID)
	if err != nil {
		t.Fatalf("InternalPort() error = %v", err)
	}

	internalDone := startMockSOCKS5Server(t, internalPort, "demo-user", "demo-pass")

	mux, serveDone := startTestMux(t, poolSvc, http.NewServeMux())
	defer stopTestMux(t, mux, serveDone)

	conn, err := net.Dial("tcp", mux.Addr().String())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatalf("write SOCKS5 greeting: %v", err)
	}
	greetingResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, greetingResp); err != nil {
		t.Fatalf("read SOCKS5 greeting response: %v", err)
	}
	if string(greetingResp) != string([]byte{0x05, 0x02}) {
		t.Fatalf("unexpected greeting response: %v", greetingResp)
	}

	authReq := make([]byte, 0, 3+len("demo-user")+len("demo-pass"))
	authReq = append(authReq, 0x01, byte(len("demo-user")))
	authReq = append(authReq, []byte("demo-user")...)
	authReq = append(authReq, byte(len("demo-pass")))
	authReq = append(authReq, []byte("demo-pass")...)
	if _, err := conn.Write(authReq); err != nil {
		t.Fatalf("write SOCKS5 auth request: %v", err)
	}
	authResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, authResp); err != nil {
		t.Fatalf("read SOCKS5 auth response: %v", err)
	}
	if string(authResp) != string([]byte{0x01, 0x00}) {
		t.Fatalf("unexpected auth response: %v", authResp)
	}

	connectReq := []byte{0x05, 0x01, 0x00, 0x01, 1, 1, 1, 1, 0, 80}
	if _, err := conn.Write(connectReq); err != nil {
		t.Fatalf("write SOCKS5 connect request: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read SOCKS5 connect reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("unexpected SOCKS5 reply: %v", reply)
	}

	if err := <-internalDone; err != nil {
		t.Fatalf("mock SOCKS5 server error = %v", err)
	}
}

func newTestPoolService(t *testing.T, poolID int64, username, password string) (*pools.Service, models.ProxyPool) {
	t.Helper()

	ctx := context.Background()
	tempDir := t.TempDir()
	cfg := config.App{
		PanelHost:               "127.0.0.1",
		PanelPort:               7890,
		DataDir:                 tempDir,
		DBPath:                  filepath.Join(tempDir, "app.db"),
		RuntimeDir:              filepath.Join(tempDir, "runtime"),
		ProdConfigPath:          filepath.Join(tempDir, "runtime", "mihomo-prod.yaml"),
		ProbeConfigPath:         filepath.Join(tempDir, "runtime", "mihomo-probe.yaml"),
		ProdControllerAddr:      "127.0.0.1:19090",
		ProbeControllerAddr:     "127.0.0.1:19091",
		ProbeMixedPort:          17891,
		DefaultControllerSecret: "secret-token",
	}
	if err := config.EnsureDirs(cfg); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}

	store, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	settingsSvc := settings.NewService(store, cfg)
	hash, err := auth.HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := settingsSvc.EnsureDefaults(ctx, hash); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	broker := events.NewBroker()
	nodeSvc := nodes.NewService(store, broker)
	subSvc := subscriptions.NewService(store, settingsSvc, broker)
	mihomoMgr := mihomo.NewManager(mihomo.Options{
		BinaryPath:          "nonexistent-mihomo-binary",
		RuntimeDir:          cfg.RuntimeDir,
		ProdConfigPath:      cfg.ProdConfigPath,
		ProbeConfigPath:     cfg.ProbeConfigPath,
		ProdControllerAddr:  cfg.ProdControllerAddr,
		ProbeControllerAddr: cfg.ProbeControllerAddr,
		ProbeMixedPort:      cfg.ProbeMixedPort,
	})
	poolSvc := pools.NewService(store, settingsSvc, nodeSvc, subSvc, mihomoMgr, broker)

	now := time.Now().UTC()
	_, err = store.DB.ExecContext(ctx, `INSERT INTO proxy_pools (
		id, name, auth_username, auth_password_secret, strategy, failover_enabled, enabled, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		poolID, "test-pool", username, password, "round_robin", 1, 1, now, now,
	)
	if err != nil {
		t.Fatalf("insert test pool error = %v", err)
	}
	pool, err := poolSvc.Get(ctx, poolID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	return poolSvc, pool
}

func startTestMux(t *testing.T, poolSvc *pools.Service, handler http.Handler) (*Mux, <-chan error) {
	t.Helper()

	mux, err := NewMux(poolSvc, handler, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewMux() error = %v", err)
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- mux.Serve()
	}()
	return mux, serveDone
}

func stopTestMux(t *testing.T, mux *Mux, serveDone <-chan error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mux.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mux shutdown")
	}
}

func startMockHTTPProxyServer(t *testing.T, port int, requests chan<- string) <-chan error {
	t.Helper()

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		defer close(done)
		defer ln.Close()

		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

		request, err := readUntil(conn, "\r\n\r\n")
		if err != nil {
			done <- err
			return
		}
		requests <- request

		_, err = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\nContent-Length: 2\r\n\r\nOK")
		done <- err
	}()
	return done
}

func startMockSOCKS5Server(t *testing.T, port int, expectedUsername, expectedPassword string) <-chan error {
	t.Helper()

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		defer close(done)
		defer ln.Close()

		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

		greeting := make([]byte, 4)
		if _, err := io.ReadFull(conn, greeting); err != nil {
			done <- err
			return
		}
		if greeting[0] != 0x05 || greeting[1] != 0x02 {
			done <- fmt.Errorf("unexpected greeting: %v", greeting)
			return
		}
		if _, err := conn.Write([]byte{0x05, 0x02}); err != nil {
			done <- err
			return
		}

		authHeader := make([]byte, 2)
		if _, err := io.ReadFull(conn, authHeader); err != nil {
			done <- err
			return
		}
		if authHeader[0] != 0x01 {
			done <- fmt.Errorf("unexpected auth version: %v", authHeader[0])
			return
		}
		username := make([]byte, authHeader[1])
		if _, err := io.ReadFull(conn, username); err != nil {
			done <- err
			return
		}
		passwordLength := make([]byte, 1)
		if _, err := io.ReadFull(conn, passwordLength); err != nil {
			done <- err
			return
		}
		password := make([]byte, passwordLength[0])
		if _, err := io.ReadFull(conn, password); err != nil {
			done <- err
			return
		}
		if string(username) != expectedUsername || string(password) != expectedPassword {
			done <- fmt.Errorf("unexpected credentials: %q/%q", string(username), string(password))
			return
		}
		if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
			done <- err
			return
		}

		requestHeader := make([]byte, 4)
		if _, err := io.ReadFull(conn, requestHeader); err != nil {
			done <- err
			return
		}
		if requestHeader[0] != 0x05 || requestHeader[1] != 0x01 {
			done <- fmt.Errorf("unexpected SOCKS5 request header: %v", requestHeader)
			return
		}
		if err := consumeSOCKS5Address(conn, requestHeader[3]); err != nil {
			done <- err
			return
		}
		_, err = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 80})
		done <- err
	}()
	return done
}

func consumeSOCKS5Address(conn net.Conn, atyp byte) error {
	var remaining int
	switch atyp {
	case 0x01:
		remaining = 6
	case 0x04:
		remaining = 18
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return err
		}
		remaining = int(length[0]) + 2
	default:
		return fmt.Errorf("unsupported atyp: %d", atyp)
	}
	buf := make([]byte, remaining)
	_, err := io.ReadFull(conn, buf)
	return err
}

func readUntil(conn net.Conn, delimiter string) (string, error) {
	reader := bufio.NewReader(conn)
	var builder strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		builder.WriteString(line)
		if strings.Contains(builder.String(), delimiter) {
			return builder.String(), nil
		}
	}
}
