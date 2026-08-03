package subscriptions

import (
	"bufio"
	"encoding/base64"
	"errors"
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
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		node, err := nodes.ParseNodeURI(line)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	if len(result.Nodes) == 0 && len(result.Errors) == 0 {
		result.Errors = append(result.Errors, errors.New("no nodes parsed"))
	}
	return result
}

func decodeMaybeBase64(input string) string {
	raw := strings.ReplaceAll(normalizeSubscriptionContent(input), "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(raw)
		if err != nil {
			return ""
		}
	}
	return string(decoded)
}

func normalizeSubscriptionContent(input string) string {
	input = strings.TrimSpace(input)
	input = strings.TrimPrefix(input, "\uFEFF")
	input = strings.ReplaceAll(input, "\r\n", "\n")
	return input
}
