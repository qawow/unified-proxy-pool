package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"unified-proxy-pool/internal/config"
)

type Options struct {
	BinaryPath          string
	RuntimeDir          string
	ProdConfigPath      string
	ProbeConfigPath     string
	ProdControllerAddr  string
	ProbeControllerAddr string
	ProbeMixedPort      int
	InitialLogLevel     string
}

type Manager struct {
	opts       Options
	httpClient *http.Client

	mu           sync.Mutex
	binaryPath   string
	prodCmd      *exec.Cmd
	probeCmd     *exec.Cmd
	hasBinary    bool
	lastSecret   string
	stopping     bool
	prodBackoff  time.Duration
	probeBackoff time.Duration
	expectedExit map[int]struct{}
}

func NewManager(opts Options) *Manager {
	resolvedBinary := opts.BinaryPath
	hasBinary := false
	if path, err := exec.LookPath(opts.BinaryPath); err == nil {
		resolvedBinary = path
		opts.BinaryPath = path
		hasBinary = true
	}
	return &Manager{
		opts:       opts,
		binaryPath: resolvedBinary,
		hasBinary:  hasBinary,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		expectedExit: make(map[int]struct{}),
	}
}

func (m *Manager) Start(ctx context.Context, secret string) error {
	if err := os.MkdirAll(m.opts.RuntimeDir, 0o755); err != nil {
		return err
	}

	m.mu.Lock()
	m.lastSecret = secret
	m.stopping = false
	m.mu.Unlock()

	if _, err := os.Stat(m.opts.ProdConfigPath); errors.Is(err, os.ErrNotExist) {
		if err := writeFileAtomic(m.opts.ProdConfigPath, minimalProdConfig(secret, m.opts.ProdControllerAddr, m.opts.InitialLogLevel)); err != nil {
			return err
		}
	}
	if _, err := os.Stat(m.opts.ProbeConfigPath); errors.Is(err, os.ErrNotExist) {
		if err := writeFileAtomic(m.opts.ProbeConfigPath, minimalProbeConfig(secret, m.opts.ProbeControllerAddr, m.opts.ProbeMixedPort, m.opts.InitialLogLevel)); err != nil {
			return err
		}
	}
	if !m.binaryAvailable() {
		return nil
	}
	if err := m.ensureProcess(ctx, "prod"); err != nil {
		return err
	}
	if err := m.ensureProcess(ctx, "probe"); err != nil {
		return err
	}
	return nil
}

func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopping = true
	prodCmd := m.prodCmd
	probeCmd := m.probeCmd
	m.markExpectedExitLocked(prodCmd)
	m.markExpectedExitLocked(probeCmd)
	m.prodCmd = nil
	m.probeCmd = nil
	m.mu.Unlock()

	stopCmd(prodCmd)
	stopCmd(probeCmd)
}

func (m *Manager) ProbeMixedPort() int {
	return m.opts.ProbeMixedPort
}

func (m *Manager) ProdControllerAddr() string {
	return m.opts.ProdControllerAddr
}

func (m *Manager) ProbeControllerAddr() string {
	return m.opts.ProbeControllerAddr
}

func (m *Manager) Status() RuntimeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	status := RuntimeStatus{
		HostOS:          runtime.GOOS,
		HostArch:        runtime.GOARCH,
		BinaryPath:      m.binaryPath,
		BinaryAvailable: m.hasBinary,
		RuntimeDir:      m.opts.RuntimeDir,
	}
	if m.prodCmd != nil && m.prodCmd.Process != nil {
		status.ProdRunning = true
		status.ProdPID = m.prodCmd.Process.Pid
	}
	if m.probeCmd != nil && m.probeCmd.Process != nil {
		status.ProbeRunning = true
		status.ProbePID = m.probeCmd.Process.Pid
	}
	return status
}

func (m *Manager) ActivateBinary(ctx context.Context, path, secret string) error {
	resolvedBinary, err := exec.LookPath(path)
	if err != nil {
		return err
	}

	m.Stop()

	m.mu.Lock()
	m.binaryPath = resolvedBinary
	m.hasBinary = true
	m.lastSecret = secret
	m.mu.Unlock()

	return m.Start(ctx, secret)
}

func (m *Manager) ApplyProdConfig(ctx context.Context, payload []byte) error {
	return m.applySingleConfig(ctx, "prod", m.opts.ProdConfigPath, payload)
}

func (m *Manager) ApplyProbeConfig(ctx context.Context, payload []byte) error {
	return m.applySingleConfig(ctx, "probe", m.opts.ProbeConfigPath, payload)
}

func (m *Manager) ApplyConfigBundle(ctx context.Context, prodPayload, probePayload []byte, nextSecret string) error {
	if err := writeFileAtomic(m.opts.ProdConfigPath, prodPayload); err != nil {
		return err
	}
	if err := writeFileAtomic(m.opts.ProbeConfigPath, probePayload); err != nil {
		return err
	}
	if !m.binaryAvailable() {
		m.setLastSecret(nextSecret)
		return nil
	}

	currentSecret := m.currentSecret()
	prodErr := m.reloadConfigWithSecret(ctx, false, m.opts.ProdConfigPath, prodPayload, currentSecret)
	probeErr := m.reloadConfigWithSecret(ctx, true, m.opts.ProbeConfigPath, probePayload, currentSecret)
	if prodErr != nil || probeErr != nil {
		if prodErr != nil {
			log.Printf("mihomo prod hot reload failed, falling back to restart: %v", prodErr)
		}
		if probeErr != nil {
			log.Printf("mihomo probe hot reload failed, falling back to restart: %v", probeErr)
		}
		if err := m.restartProcess(ctx, "prod"); err != nil {
			return err
		}
		if err := m.restartProcess(ctx, "probe"); err != nil {
			return err
		}
	}
	if err := m.waitControllerWithSecret(ctx, false, nextSecret); err != nil {
		return err
	}
	if err := m.waitControllerWithSecret(ctx, true, nextSecret); err != nil {
		return err
	}
	m.setLastSecret(nextSecret)
	return nil
}

func (m *Manager) applySingleConfig(ctx context.Context, kind, configPath string, payload []byte) error {
	if err := writeFileAtomic(configPath, payload); err != nil {
		return err
	}
	if !m.binaryAvailable() {
		return nil
	}
	probe := kind == "probe"
	if err := m.reloadConfigWithSecret(ctx, probe, configPath, payload, m.currentSecret()); err == nil {
		return m.waitControllerWithSecret(ctx, probe, m.currentSecret())
	} else {
		log.Printf("mihomo %s hot reload failed, falling back to restart: %v", kind, err)
	}
	if err := m.restartProcess(ctx, kind); err != nil {
		return err
	}
	return m.waitControllerWithSecret(ctx, probe, m.currentSecret())
}

func (m *Manager) Delay(ctx context.Context, secret, proxyName, targetURL string, timeoutMS int) (int64, error) {
	if !m.binaryAvailable() {
		return 0, errors.New("mihomo binary not available")
	}
	if err := m.waitController(ctx, true); err != nil {
		return 0, err
	}
	endpoint := fmt.Sprintf("http://%s/proxies/%s/delay?url=%s&timeout=%d",
		m.opts.ProbeControllerAddr,
		url.PathEscape(proxyName),
		url.QueryEscape(targetURL),
		timeoutMS,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("delay api failed: %s", strings.TrimSpace(string(body)))
	}
	var data struct {
		Delay int64 `json:"delay"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, err
	}
	return data.Delay, nil
}

func (m *Manager) SetGlobalProxy(ctx context.Context, secret, proxyName string) error {
	return m.SetProxySelection(ctx, secret, "GLOBAL", proxyName)
}

func (m *Manager) SetProxySelection(ctx context.Context, secret, groupName, proxyName string) error {
	if !m.binaryAvailable() {
		return errors.New("mihomo binary not available")
	}
	if err := m.waitController(ctx, true); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"name": proxyName})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("http://%s/proxies/%s", m.opts.ProbeControllerAddr, url.PathEscape(groupName)), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("set proxy selection failed: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func (m *Manager) waitController(ctx context.Context, probe bool) error {
	return m.waitControllerWithSecret(ctx, probe, m.currentSecret())
}

func (m *Manager) waitControllerWithSecret(ctx context.Context, probe bool, secret string) error {
	if !m.binaryAvailable() {
		return nil
	}
	addr := m.opts.ProdControllerAddr
	if probe {
		addr = m.opts.ProbeControllerAddr
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/version", addr), nil)
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		resp, err := m.httpClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			return fmt.Errorf("controller %s not ready", addr)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func (m *Manager) ensureProcess(ctx context.Context, kind string) error {
	m.mu.Lock()
	existing := m.currentCmdLocked(kind)
	m.mu.Unlock()
	if existing != nil && existing.Process != nil {
		return nil
	}
	return m.startProcess(ctx, kind)
}

func (m *Manager) startProcess(ctx context.Context, kind string) error {
	configPath := m.opts.ProdConfigPath
	if kind == "probe" {
		configPath = m.opts.ProbeConfigPath
	}

	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return context.Canceled
	}
	binaryPath := m.binaryPath
	if existing := m.currentCmdLocked(kind); existing != nil && existing.Process != nil {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	cmd := exec.CommandContext(ctx, binaryPath, "-d", m.opts.RuntimeDir, "-f", configPath)
	cmd.Dir = m.opts.RuntimeDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	m.mu.Lock()
	if kind == "prod" {
		m.prodCmd = cmd
	} else {
		m.probeCmd = cmd
	}
	m.mu.Unlock()

	log.Printf("mihomo %s started with pid %d", kind, cmd.Process.Pid)
	go m.waitProcess(kind, cmd)
	go m.resetBackoffAfterStableRun(kind, cmd.Process.Pid)
	return nil
}

func (m *Manager) restartProcess(ctx context.Context, kind string) error {
	m.mu.Lock()
	current := m.currentCmdLocked(kind)
	m.markExpectedExitLocked(current)
	if kind == "prod" {
		m.prodCmd = nil
	} else {
		m.probeCmd = nil
	}
	m.resetBackoffLocked(kind)
	m.mu.Unlock()

	stopCmd(current)
	time.Sleep(300 * time.Millisecond)
	return m.startProcess(ctx, kind)
}

func (m *Manager) reloadConfig(ctx context.Context, probe bool, configPath string, payload []byte) error {
	return m.reloadConfigWithSecret(ctx, probe, configPath, payload, m.currentSecret())
}

func (m *Manager) reloadConfigWithSecret(ctx context.Context, probe bool, configPath string, payload []byte, secret string) error {
	if err := m.waitControllerWithSecret(ctx, probe, secret); err != nil {
		return err
	}
	controllerAddr := m.opts.ProdControllerAddr
	if probe {
		controllerAddr = m.opts.ProbeControllerAddr
	}

	body, err := json.Marshal(map[string]string{
		"path":    filepath.Base(configPath),
		"payload": string(payload),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("http://%s/configs?force=true", controllerAddr), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("config reload failed: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func (m *Manager) currentSecret() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastSecret
}

func (m *Manager) binaryAvailable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hasBinary && m.binaryPath != ""
}

func (m *Manager) setLastSecret(secret string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSecret = secret
}

func (m *Manager) waitProcess(kind string, cmd *exec.Cmd) {
	err := cmd.Wait()
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	m.handleProcessExit(kind, pid, err)
}

func (m *Manager) handleProcessExit(kind string, pid int, cause error) {
	m.mu.Lock()
	if _, ok := m.expectedExit[pid]; ok {
		delete(m.expectedExit, pid)
		m.mu.Unlock()
		return
	}

	current := m.currentCmdLocked(kind)
	if current == nil || current.Process == nil || current.Process.Pid != pid {
		m.mu.Unlock()
		return
	}
	if kind == "prod" {
		m.prodCmd = nil
	} else {
		m.probeCmd = nil
	}
	if m.stopping {
		m.mu.Unlock()
		return
	}
	backoff := m.bumpBackoffLocked(kind)
	m.mu.Unlock()

	log.Printf("mihomo %s exited unexpectedly (pid=%d): %v; restarting in %s", kind, pid, cause, backoff)
	go func() {
		timer := time.NewTimer(backoff)
		defer timer.Stop()
		<-timer.C

		m.mu.Lock()
		if m.stopping || m.currentCmdLocked(kind) != nil {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		if err := m.startProcess(context.Background(), kind); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("mihomo %s restart failed: %v", kind, err)
		}
	}()
}

func (m *Manager) resetBackoffAfterStableRun(kind string, pid int) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	<-timer.C

	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.currentCmdLocked(kind)
	if current == nil || current.Process == nil || current.Process.Pid != pid {
		return
	}
	m.resetBackoffLocked(kind)
}

func (m *Manager) currentCmdLocked(kind string) *exec.Cmd {
	if kind == "prod" {
		return m.prodCmd
	}
	return m.probeCmd
}

func (m *Manager) markExpectedExitLocked(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	m.expectedExit[cmd.Process.Pid] = struct{}{}
}

func (m *Manager) bumpBackoffLocked(kind string) time.Duration {
	current := m.prodBackoff
	if kind == "probe" {
		current = m.probeBackoff
	}
	if current == 0 {
		current = time.Second
	} else {
		current *= 2
		if current > 30*time.Second {
			current = 30 * time.Second
		}
	}
	if kind == "prod" {
		m.prodBackoff = current
	} else {
		m.probeBackoff = current
	}
	return current
}

func (m *Manager) resetBackoffLocked(kind string) {
	if kind == "prod" {
		m.prodBackoff = 0
		return
	}
	m.probeBackoff = 0
}

func stopCmd(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if runtime.GOOS != "windows" {
		_ = cmd.Process.Signal(os.Interrupt)
		time.Sleep(500 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
}

func writeFileAtomic(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func minimalProdConfig(secret, controller, logLevel string) []byte {
	return []byte(fmt.Sprintf(`mode: rule
log-level: %s
allow-lan: true
external-controller: %s
secret: "%s"
proxies: []
proxy-groups: []
listeners: []
rules:
  - MATCH,DIRECT
`, config.NormalizeLogLevel(logLevel), controller, secret))
}

func minimalProbeConfig(secret, controller string, mixedPort int, logLevel string) []byte {
	return []byte(fmt.Sprintf(`mode: global
log-level: %s
allow-lan: false
mixed-port: %d
external-controller: %s
secret: "%s"
proxies: []
proxy-groups:
  - name: GLOBAL
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,GLOBAL
`, config.NormalizeLogLevel(logLevel), mixedPort, controller, secret))
}
