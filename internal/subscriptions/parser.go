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
	if parsed, errs := nodes.ParseRawNodes(content); len(parsed) > 0 {
		return ParseResult{Nodes: parsed, Errors: errs}
	}
	if decoded := decodeMaybeBase64(content); decoded != "" {
		if parsed, errs := nodes.ParseRawNodes(decoded); len(parsed) > 0 {
			return ParseResult{Nodes: parsed, Errors: errs}
		}
	}

	var result ParseResult
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		node, err := nodes.ParseNodeURI(line)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	// scanner.Err() is the only signal that a line exceeded the 1MB buffer:
	// Scan silently stops and the giant line is dropped, which would otherwise
	// surface to the operator as "no nodes parsed" with no reason attached.
	// The nodes package's parser already checks this; the subscription path
	// shared the bug until here.
	if err := scanner.Err(); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("scan stopped: %w", err))
	}
	if len(result.Nodes) == 0 && len(result.Errors) == 0 {
		result.Errors = append(result.Errors, errors.New("no nodes parsed"))
	}
	return result
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
