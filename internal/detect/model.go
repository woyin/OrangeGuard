// Package detect extracts the processing model from upstream responses,
// decides whether it is an acceptable stand-in for the requested model, and
// classifies upstream errors for cooldown purposes.
package detect

import (
	"encoding/json"
	"strings"

	"github.com/woyin/orangeguard/internal/config"
)

// modelProbe covers the shapes that carry the processing model across the
// protocols cpa bridges:
//   - OpenAI chat/completions and most compatible APIs: top-level "model"
//   - Claude: "model" (non-stream) or message.model in message_start
//   - OpenAI Responses API: response.model in response.created events
//   - Gemini: top-level "modelVersion"
type modelProbe struct {
	Model        string `json:"model"`
	ModelVersion string `json:"modelVersion"`
	Message      struct {
		Model string `json:"model"`
	} `json:"message"`
	Response struct {
		Model        string `json:"model"`
		ModelVersion string `json:"modelVersion"`
	} `json:"response"`
}

func extractFromJSON(payload []byte) string {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" || trimmed[0] != '{' {
		return ""
	}
	var probe modelProbe
	if errUnmarshal := json.Unmarshal([]byte(trimmed), &probe); errUnmarshal != nil {
		return ""
	}
	for _, candidate := range []string{
		probe.Model, probe.Message.Model, probe.Response.Model,
		probe.ModelVersion, probe.Response.ModelVersion,
	} {
		if value := strings.TrimSpace(candidate); value != "" {
			return value
		}
	}
	return ""
}

func extractFromSSE(payload []byte) string {
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		if model := extractFromJSON([]byte(data)); model != "" {
			return model
		}
	}
	return ""
}

// ProcessingModel returns the model that actually served a response. It
// accepts a raw JSON body, an SSE payload, or a JSON array (Gemini streams
// without SSE framing). Empty means the response did not name a model.
func ProcessingModel(payload []byte) string {
	if model := extractFromJSON(payload); model != "" {
		return model
	}
	if model := extractFromSSE(payload); model != "" {
		return model
	}
	trimmed := strings.TrimSpace(string(payload))
	if strings.HasPrefix(trimmed, "[") {
		var items []json.RawMessage
		if errUnmarshal := json.Unmarshal([]byte(trimmed), &items); errUnmarshal == nil {
			for _, item := range items {
				if model := extractFromJSON(item); model != "" {
					return model
				}
			}
		}
	}
	return ""
}

// Expectation describes which processing models are acceptable.
type Expectation struct {
	// Accept lists acceptable names; globs match literally, plain names use the
	// tolerant matcher. Empty means Default is expected.
	Accept []string
	// Deny lists names that are always rejected, even if accepted above.
	Deny []string
	// Default is the expected name when Accept is empty.
	Default string
	// RejectMissing rejects responses that report no model at all.
	RejectMissing bool
}

// Verdict is the result of checking a processing model.
type Verdict int

const (
	// VerdictAccepted means the response may be delivered.
	VerdictAccepted Verdict = iota
	// VerdictMismatch means a different model served the request.
	VerdictMismatch
	// VerdictUnknown means the response named no model and that is tolerated.
	VerdictUnknown
)

// Check decides whether actual is acceptable.
func (e Expectation) Check(actual string) Verdict {
	actual = strings.TrimSpace(actual)
	if actual == "" {
		if e.RejectMissing {
			return VerdictMismatch
		}
		return VerdictUnknown
	}
	for _, pattern := range e.Deny {
		if config.Glob(pattern, actual) {
			return VerdictMismatch
		}
	}
	accept := e.Accept
	if len(accept) == 0 {
		accept = []string{e.Default}
	}
	for _, pattern := range accept {
		if config.HasWildcard(pattern) {
			if config.Glob(pattern, actual) {
				return VerdictAccepted
			}
			continue
		}
		if Matches(pattern, actual) {
			return VerdictAccepted
		}
	}
	return VerdictMismatch
}

// Matches reports whether actual is an acceptable stand-in for expected.
//
// Beyond case-insensitive equality it tolerates:
//   - a version/date suffix appended upstream (gpt-6-astra -> gpt-6-astra-2026-08-01)
//   - an alias prefix stripped by cpa (my-glm-5.2 -> glm-5.2)
//   - a provider prefix added upstream (gpt-6-astra -> openai/gpt-6-astra)
//
// Every relaxation requires a separator at the boundary, and an appended
// suffix must look like a snapshot/date, so gpt-6-astra never matches
// gpt-6-astra-mini and gpt-5 never matches gpt-5.5-mini.
func Matches(expected, actual string) bool {
	exp := strings.ToLower(strings.TrimSpace(expected))
	act := strings.ToLower(strings.TrimSpace(actual))
	if exp == "" || act == "" {
		return false
	}
	if exp == act {
		return true
	}
	// Provider prefix on the actual name: "openai/gpt-6-astra".
	if i := strings.LastIndex(act, "/"); i >= 0 && !strings.Contains(exp, "/") {
		if Matches(exp, act[i+1:]) {
			return true
		}
	}
	// Version/date suffix appended upstream.
	if strings.HasPrefix(act, exp) && isSuffixBoundary(act[len(exp)]) && isVersionSuffix(act[len(exp)+1:]) {
		return true
	}
	// Alias prefix stripped: expected "my-glm-5.2", actual "glm-5.2".
	if strings.HasSuffix(exp, act) && isBoundary(exp[len(exp)-len(act)-1]) {
		return true
	}
	return false
}

func isBoundary(c byte) bool {
	return c == '-' || c == '_' || c == '.' || c == '/' || c == ':' || c == '@'
}

// isSuffixBoundary excludes '.', which introduces a minor version
// (gpt-5 -> gpt-5.5) rather than a snapshot of the same model.
func isSuffixBoundary(c byte) bool {
	return c == '-' || c == '_' || c == ':' || c == '@'
}

// isVersionSuffix accepts snapshot suffixes such as "0722", "2026-08-01",
// "001", "20250219" or "latest"/"preview". It rejects tier names ("mini",
// "nano", "flash", "lite", "turbo", "5-mini") and short version bumps ("1" in
// claude-opus-4-1), which indicate a different model.
func isVersionSuffix(suffix string) bool {
	if suffix == "" {
		return false
	}
	digits := 0
	for _, part := range strings.FieldsFunc(suffix, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ':' || r == '@'
	}) {
		switch part {
		case "latest", "preview", "exp", "experimental", "beta", "stable", "release":
			continue
		}
		if !isDigits(part) {
			return false
		}
		digits += len(part)
	}
	return digits == 0 || digits >= 3
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
