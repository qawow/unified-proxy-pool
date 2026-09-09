package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"unified-proxy-pool/internal/version"
)

const (
	DefaultRepo    = "qawow/unified-proxy-pool"
	DefaultTag     = "nightly"
	minBinaryBytes = 2 << 20
	elfMagic       = "\x7fELF"
)

var execFn = execSelf

type Status struct {
	LocalCommit  string `json:"local_commit"`
	LocalShort   string `json:"local_short"`
	LocalTime    string `json:"local_time"`
	RemoteCommit string `json:"remote_commit"`
	RemoteShort  string `json:"remote_short"`
	UpdateURL    string `json:"update_url"`
	Newer        bool   `json:"newer"`
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
}

// Service downloads a replacement binary from the GitHub release channel.
//
// Trust model: the panel may sit behind a censored link, so downloads can fall
// back to the free-proxy pool or a public GitHub mirror — neither of which is
// trusted. Therefore the *checksum* is only ever fetched over a channel with
// certificate verification enabled (direct, or CONNECT-tunnelled through a
// proxy, both of which an on-path attacker cannot forge), while the much larger
// binary may come from anywhere and is accepted only if it matches that
// checksum. If the checksum cannot be obtained over a verified channel the
// update is refused rather than downgraded to "hope".
type Service struct {
	repo    string
	tag     string
	baseURL string // empty = https://github.com
	client  *http.Client
	// fallbackFn resolves the through-the-pool client lazily; the pool changes
	// over the process lifetime, so it must not be captured once at construction.
	fallbackFn func() *http.Client
	mu         sync.Mutex
	updating   bool
}

func New(repo, tag string, client *http.Client, fallbackFn func() *http.Client) *Service {
	if repo == "" {
		repo = DefaultRepo
	}
	if tag == "" {
		tag = DefaultTag
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Service{repo: repo, tag: tag, client: client, fallbackFn: fallbackFn}
}

func (s *Service) fallback() *http.Client {
	if s == nil || s.fallbackFn == nil {
		return nil
	}
	return s.fallbackFn()
}

func (s *Service) Check(ctx context.Context) (Status, error) {
	st := Status{
		LocalCommit: version.Commit,
		LocalShort:  version.Short(),
		LocalTime:   version.Time,
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		UpdateURL:   s.binaryURL(),
	}
	remote, err := s.fetchTrustedText(ctx, s.versionURL())
	if err != nil {
		return st, err
	}
	st.RemoteCommit = strings.TrimSpace(strings.Split(remote, "\n")[0])
	st.RemoteShort = st.RemoteCommit
	if len(st.RemoteShort) > 7 {
		st.RemoteShort = st.RemoteShort[:7]
	}
	st.Newer = isNewer(version.Commit, st.RemoteCommit)
	return st, nil
}

func isNewer(local, remote string) bool {
	local = strings.TrimSpace(local)
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return false
	}
	if local == "" || local == "dev" {
		return true
	}
	if strings.EqualFold(local, remote) {
		return false
	}
	if strings.HasPrefix(local, remote) || strings.HasPrefix(remote, local) {
		return false
	}
	return true
}

// Prepare downloads, verifies and installs the new binary in place. It does not
// restart: the caller flushes its HTTP response first and then calls Restart,
// otherwise the exec would replace the process before the client learns the
// outcome.
func (s *Service) Prepare(ctx context.Context) (Status, error) {
	s.mu.Lock()
	if s.updating {
		s.mu.Unlock()
		return Status{}, fmt.Errorf("update already running")
	}
	s.updating = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.updating = false
		s.mu.Unlock()
	}()

	st, err := s.Check(ctx)
	if err != nil {
		return st, err
	}
	if !st.Newer {
		return st, fmt.Errorf("already up to date (%s)", version.Short())
	}
	wantSum, err := s.fetchChecksum(ctx)
	if err != nil {
		return st, err
	}
	body, err := s.fetchBytes(ctx, st.UpdateURL)
	if err != nil {
		return st, err
	}
	if err := validateBinary(body, wantSum); err != nil {
		return st, err
	}
	exe, err := os.Executable()
	if err != nil {
		return st, err
	}
	if resolved, err2 := filepath.EvalSymlinks(exe); err2 == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	tmp := filepath.Join(dir, ".unified-proxy-pool.new")
	if err := os.WriteFile(tmp, body, 0o755); err != nil { //nolint:gosec // must stay executable
		return st, fmt.Errorf("write new binary: %w", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return st, fmt.Errorf("replace %s: %w", exe, err)
	}
	return st, nil
}

// Restart execs the on-disk binary, replacing this process image. It never
// returns on success.
func (s *Service) Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err2 := filepath.EvalSymlinks(exe); err2 == nil {
		exe = resolved
	}
	return execFn(exe, os.Args, os.Environ())
}

// Apply is Prepare followed immediately by Restart; kept for callers that do
// not need to answer an HTTP request first.
func (s *Service) Apply(ctx context.Context) (Status, error) {
	st, err := s.Prepare(ctx)
	if err != nil {
		return st, err
	}
	if err := s.Restart(); err != nil {
		return st, fmt.Errorf("exec new binary: %w", err)
	}
	return st, nil
}

// fetchChecksum reads "<sha256>  unified-proxy-pool" from the release, over a
// certificate-verified channel only.
func (s *Service) fetchChecksum(ctx context.Context) (string, error) {
	raw, err := s.fetchTrustedText(ctx, s.checksumURL())
	if err != nil {
		return "", fmt.Errorf("checksum unavailable, refusing to update: %w", err)
	}
	sum := parseChecksum(raw)
	if sum == "" {
		return "", fmt.Errorf("checksum file is malformed, refusing to update")
	}
	return sum, nil
}

func parseChecksum(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		candidate := strings.TrimPrefix(fields[0], "\\")
		if len(candidate) != 64 {
			continue
		}
		if _, err := hex.DecodeString(candidate); err != nil {
			continue
		}
		return strings.ToLower(candidate)
	}
	return ""
}

func validateBinary(body []byte, wantSum string) error {
	if len(body) < minBinaryBytes {
		return fmt.Errorf("downloaded binary too small (%d bytes)", len(body))
	}
	if runtime.GOOS == "linux" && (len(body) < 4 || string(body[:4]) != elfMagic) {
		return fmt.Errorf("downloaded file is not a linux ELF binary")
	}
	if wantSum == "" {
		return fmt.Errorf("no checksum to verify against")
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != strings.ToLower(wantSum) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", hex.EncodeToString(got[:]), wantSum)
	}
	return nil
}

func (s *Service) githubBase() string {
	if s.baseURL != "" {
		return strings.TrimRight(s.baseURL, "/")
	}
	return "https://github.com"
}

func (s *Service) versionURL() string {
	return fmt.Sprintf("%s/%s/releases/download/%s/version.txt", s.githubBase(), s.repo, s.tag)
}

func (s *Service) checksumURL() string {
	return fmt.Sprintf("%s/%s/releases/download/%s/unified-proxy-pool.sha256", s.githubBase(), s.repo, s.tag)
}

func (s *Service) binaryURL() string {
	return fmt.Sprintf("%s/%s/releases/download/%s/unified-proxy-pool", s.githubBase(), s.repo, s.tag)
}

func (s *Service) fetchTrustedText(ctx context.Context, raw string) (string, error) {
	b, err := s.fetchTrusted(ctx, raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// fetchTrusted goes direct first, then through the pool — both with certificate
// verification, so the pool node cannot tamper with the bytes. The plaintext
// mirror is deliberately not consulted here.
func (s *Service) fetchTrusted(ctx context.Context, raw string) ([]byte, error) {
	b, err := s.doGet(ctx, s.client, raw)
	if err == nil {
		return b, nil
	}
	if fb := s.fallback(); fb != nil {
		if b2, err2 := s.doGet(ctx, fb, raw); err2 == nil {
			return b2, nil
		}
	}
	return nil, err
}

// fetchBytes may fall back to a public mirror; callers must verify the payload
// against a checksum obtained via fetchTrusted.
func (s *Service) fetchBytes(ctx context.Context, raw string) ([]byte, error) {
	b, err := s.fetchTrusted(ctx, raw)
	if err == nil {
		return b, nil
	}
	mirror := "https://ghproxy.net/https://" + strings.TrimPrefix(raw, "https://")
	if fb := s.fallback(); fb != nil {
		if b2, err2 := s.doGet(ctx, fb, mirror); err2 == nil {
			return b2, nil
		}
	}
	if b2, err2 := s.doGet(ctx, s.client, mirror); err2 == nil {
		return b2, nil
	}
	return nil, err
}

func (s *Service) doGet(ctx context.Context, client *http.Client, raw string) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("no http client")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "unified-proxy-pool-update")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d from %s", resp.StatusCode, raw)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 80<<20))
}
