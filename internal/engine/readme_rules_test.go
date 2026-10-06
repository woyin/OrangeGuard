package engine

import (
	"testing"

	"github.com/woyin/orangeguard/internal/config"
	"github.com/woyin/orangeguard/internal/detect"
)

// The request-selector glob must never become the accepted-response glob.
func TestREADMEIdentityWithEmptyExpect(t *testing.T) {
	for _, c := range []struct {
		request, actual string
		want            detect.Verdict
	}{
		{"gpt-6-astra", "gpt-6-astra", detect.VerdictAccepted},
		{"gpt-6-astra", "gpt-6-astra-2026-08-01", detect.VerdictAccepted},
		{"gpt-6-astra", "openai/gpt-6-astra", detect.VerdictAccepted},
		{"cline-pass/deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash", detect.VerdictAccepted},
		{"deepseek-v4.1-pro", "deepseek-v4.1-flash", detect.VerdictMismatch},
		{"glm-4.5", "glm-4.5-air", detect.VerdictMismatch},
		{"my-glm-5.2", "glm-5.2", detect.VerdictAccepted},
		{"gpt-6-astra", "gpt-5.5-mini", detect.VerdictMismatch},
		{"gpt-6-astra", "gpt-6-astra-mini", detect.VerdictMismatch},
		{"gpt-5", "gpt-5.5", detect.VerdictMismatch},
		{"claude-opus-4", "claude-opus-4-1", detect.VerdictMismatch},
		{"glm-5.2", "glm-5.21", detect.VerdictMismatch},
	} {
		t.Run(c.request+"/"+c.actual, func(t *testing.T) {
			e := expectationFromRule(config.GuardRule{Model: "*"}, c.request)
			if got := e.Check(c.actual); got != c.want {
				t.Fatalf("empty expect for request %q: actual=%q got=%v want=%v", c.request, c.actual, got, c.want)
			}
		})
	}
}

func TestDefaultGuardScopeIsNotAllREADMEModels(t *testing.T) {
	e := newEngine(t, newFakeHost(nil), "guard:\n  max_retries: 0\n")
	for _, model := range []string{"my-glm-5.2", "claude-opus-4"} {
		if claimed, _ := e.Claims(model, false); claimed {
			t.Fatalf("default guard unexpectedly claims %q", model)
		}
	}
}
