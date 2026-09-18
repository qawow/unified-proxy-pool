package subscriptions

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"unified-proxy-pool/internal/nodes"
)

type ParseResult struct {
	Nodes  []nodes.ParsedNode
	Errors []error
}

func ParseSubscriptionContent(content string) ParseResult {
	content = normalizeSubscriptionContent(content)
	if content == "" {
		return ParseResult{Errors: []error{errors.New("subscription content is empty")}}
	}
	if looksLikeHTML(content) {
		return ParseResult{Errors: []error{errors.New("got HTML instead of a subscription")}}
	}
	// A line that is not itself a node URI may be a base64 blob holding more
	// URIs: feeds mix plaintext and encoded lines, and a whole-blob
	// subscription is just the one-line case. Expanding first keeps a single
	// parse path — the old two-stage parse returned early on the first good
	// plaintext node and silently dropped every base64 line's nodes.
	expanded, scanErr := expandBase64Lines(content)
	parsed, errs := nodes.ParseRawNodes(expanded)
	if scanErr != nil {
		errs = append(errs, fmt.Errorf("scan stopped: %w", scanErr))
	}
	if len(parsed) == 0 && len(errs) == 0 {
		errs = []error{errors.New("no nodes parsed")}
	}
	return ParseResult{Nodes: parsed, Errors: errs}
}

// expandBase64Lines splices each base64-encoded line into the URIs it holds.
// Lines that do not decode into node-looking content are kept as-is so their
// parse errors still point at the original text. The returned error is the
// scanner's — a line longer than the 1MB buffer stops Scan silently, and
// swallowing that made a giant line look like "no nodes parsed".
func expandBase64Lines(content string) (string, error) {
	var out strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if decoded := decodeMaybeBase64(line); decoded != "" && looksLikeNodeList(decoded) {
			out.WriteString(decoded)
			out.WriteString("\n")
			continue
		}
		// Keep the untrimmed line: Clash YAML depends on indentation, and the
		// trimmed form flattened `  - {…}` list items to column 0.
		out.WriteString(raw)
		out.WriteString("\n")
	}
	return out.String(), scanner.Err()
}

// looksLikeNodeList is the cheap gate for splicing decoded content: every
// supported node URI carries a scheme separator, and Clash YAML names the
// proxies list.
func looksLikeNodeList(content string) bool {
	return strings.Contains(content, "://") || strings.Contains(content, "proxies:")
}

func looksLikeHTML(content string) bool {
	s := strings.TrimSpace(content)
	if len(s) > 256 {
		s = s[:256]
	}
	low := strings.ToLower(s)
	return strings.HasPrefix(low, "<!doctype html") || strings.HasPrefix(low, "<html") ||
		(strings.Contains(low, "<head>") && strings.Contains(low, "<body"))
}

func decodeMaybeBase64(input string) string {
	raw := strings.ReplaceAll(normalizeSubscriptionContent(input), "\n", "")
	raw = strings.ReplaceAll(raw, " ", "")
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(raw)
		if err == nil && len(decoded) > 0 {
			return string(decoded)
		}
	}
	return ""
}

func normalizeSubscriptionContent(input string) string {
	input = strings.TrimSpace(input)
	input = strings.TrimPrefix(input, "\uFEFF")
	input = strings.ReplaceAll(input, "\r\n", "\n")
	return input
}
