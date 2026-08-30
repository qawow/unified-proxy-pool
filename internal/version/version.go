package version

import "strings"

// Set at link time: -ldflags "-X unified-proxy-pool/internal/version.Commit=..."
var (
	Commit = "dev"
	Time   = ""
)

func Short() string {
	c := strings.TrimSpace(Commit)
	if c == "" {
		return "dev"
	}
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

func Info() map[string]string {
	return map[string]string{
		"commit": Commit,
		"short":  Short(),
		"time":   Time,
	}
}
