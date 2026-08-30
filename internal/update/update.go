package update

import (
	"context"
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

type Service struct {
	repo     string
	tag      string
	baseURL  string // empty = https://github.com
	client   *http.Client
	fallback *http.Client
	mu       sync.Mutex
	updating bool
}

func New(repo, tag string, client, fallback *http.Client) *Service {
	if repo == "" {
		repo = DefaultRepo
	}
	if tag == "" {
		tag = DefaultTag
	}
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	return &Service{repo: repo, tag: tag, client: client, fallback: fallback}
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
	remote, err := s.fetchText(ctx, s.versionURL())
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

func (s *Service) Apply(ctx context.Context) (Status, error) {
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
	body, err := s.fetchBytes(ctx, st.UpdateURL)
	if err != nil {
		return st, err
	}
	if err := validateBinary(body); err != nil {
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
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		return st, fmt.Errorf("write new binary: %w", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		if err2 := execFn(tmp, os.Args, os.Environ()); err2 != nil {
			return st, fmt.Errorf("replace %s: %v; exec: %v", exe, err, err2)
		}
		return st, nil
	}
	if err := execFn(exe, os.Args, os.Environ()); err != nil {
		return st, fmt.Errorf("exec new binary: %w", err)
	}
	return st, nil
}

func validateBinary(body []byte) error {
	if len(body) < minBinaryBytes {
		return fmt.Errorf("downloaded binary too small (%d bytes)", len(body))
	}
	if runtime.GOOS == "linux" && (len(body) < 4 || string(body[:4]) != elfMagic) {
		return fmt.Errorf("downloaded file is not a linux ELF binary")
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

func (s *Service) binaryURL() string {
	return fmt.Sprintf("%s/%s/releases/download/%s/unified-proxy-pool", s.githubBase(), s.repo, s.tag)
}

func (s *Service) fetchText(ctx context.Context, raw string) (string, error) {
	b, err := s.fetchBytes(ctx, raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Service) fetchBytes(ctx context.Context, raw string) ([]byte, error) {
	b, err := s.doGet(ctx, s.client, raw)
	if err == nil {
		return b, nil
	}
	mirror := "https://ghproxy.net/https://" + strings.TrimPrefix(raw, "https://")
	if s.fallback != nil {
		if b2, err2 := s.doGet(ctx, s.fallback, raw); err2 == nil {
			return b2, nil
		}
		if b2, err2 := s.doGet(ctx, s.fallback, mirror); err2 == nil {
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
