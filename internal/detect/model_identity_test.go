package detect

import "testing"

func TestNamespacedModelIdentity(t *testing.T) {
	cases := []struct {
		expected, actual string
		want             bool
	}{
		{"cline-pass/deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash", true},
		{"cline-pass/deepseek-v4.1-flash", "deepseek-v4.1-flash", true},
		{"deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash", true},
		{"router/cline-pass/deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash-20260801", true},
		{"models/gemini-3-pro", "gemini-3-pro", true},
		{"openai/gpt-5", "other/gpt-5-2025-08-07", true},
		{"z-ai/glm-4.5", "GLM-4.5", true},
		{"channel/my-glm-5.2", "z-ai/glm-5.2", true},
		{"cline-pass/deepseek-v4.1-pro", "deepseek/deepseek-v4.1-flash", false},
		{"deepseek/deepseek-v4.1-flash", "deepseek/deepseek-v4-flash", false},
		{"deepseek-chat", "deepseek-reasoner", false},
		{"deepseek-chat", "deepseek-v4-flash", false},
		{"glm-4.5", "z-ai/glm-4.5-air", false},
		{"glm-4.5-air", "glm-4.5-airx", false},
		{"glm-4.7", "glm-4.7-flashx", false},
		{"openai/gpt-5-mini", "mini", false},
		{"deepseek-reasoner", "reasoner", false},
		{"a/gpt-5", "b/gpt-5.1", false},
		{"gpt-5", "gpt-5---", false},
		{"a/", "b/", false},
	}
	for _, c := range cases {
		if got := Matches(c.expected, c.actual); got != c.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", c.expected, c.actual, got, c.want)
		}
	}
}

func TestNamespacedExpectation(t *testing.T) {
	for _, c := range []struct {
		e      Expectation
		actual string
		want   Verdict
	}{
		{Expectation{Default: "cline-pass/deepseek-v4.1-flash"}, "deepseek/deepseek-v4.1-flash", VerdictAccepted},
		{Expectation{Accept: []string{"glm-*"}, Deny: []string{"glm-*-air"}}, "z-ai/glm-4.5-air", VerdictMismatch},
		{Expectation{Accept: []string{"glm-*"}}, "z-ai/glm-4.5", VerdictAccepted},
		{Expectation{Accept: []string{"glm-4.5"}, Deny: []string{"glm-4.5"}}, "z-ai/glm-4.5", VerdictMismatch},
		{Expectation{Accept: []string{"openai/gpt-*"}}, "other/gpt-5", VerdictMismatch},
		{Expectation{Default: "a/gpt-5", RejectMissing: true}, "", VerdictMismatch},
	} {
		if got := c.e.Check(c.actual); got != c.want {
			t.Errorf("%+v.Check(%q) = %v, want %v", c.e, c.actual, got, c.want)
		}
	}
}

func TestCommonProviderModelFields(t *testing.T) {
	for _, c := range []struct{ payload, want string }{
		{`{"model":"deepseek-v4-flash","choices":[]}`, "deepseek-v4-flash"},
		{"data: {\"model\":\"glm-4.5-air\",\"choices\":[{\"delta\":{}}]}\r\n\r\n", "glm-4.5-air"},
		{`{"object":"response","model":"gpt-5","output":[]}`, "gpt-5"},
		{"event: response.completed\ndata: {\"response\":{\"model\":\"gpt-5\"}}\n\n", "gpt-5"},
		{`{"message":{"model":"deepseek-v4-flash"}}`, "deepseek-v4-flash"},
		// Never search generated content or usage/diagnostic fields for identity.
		{`{"choices":[{"message":{"content":"model: gpt-5"}}],"usage":{"model":"gpt-5"},"system_fingerprint":"gpt-5"}`, ""},
	} {
		if got := ProcessingModel([]byte(c.payload)); got != c.want {
			t.Errorf("ProcessingModel(%q) = %q, want %q", c.payload, got, c.want)
		}
	}
}
