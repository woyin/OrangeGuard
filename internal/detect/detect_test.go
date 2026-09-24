package detect

import (
	"testing"
	"time"
)

func TestMatches(t *testing.T) {
	cases := []struct {
		expected, actual string
		want             bool
	}{
		{"gpt-6-astra", "gpt-6-astra", true},
		{"GPT-6-Astra", "gpt-6-astra", true},
		{"gpt-6-astra", "gpt-6-astra-2026-08-01", true},
		{"glm-5.2", "glm-5.2-0722", true},
		{"claude-opus-5", "claude-opus-5-20260301", true},
		{"gpt-6-astra", "gpt-6-astra-latest", true},
		{"my-glm-5.2", "glm-5.2", true},
		{"gpt-6-astra", "openai/gpt-6-astra", true},
		// Downgrades and neighbours must be rejected.
		{"gpt-6-astra", "gpt-5.5-mini", false},
		{"gpt-6-astra", "gpt-6-astra-mini", false},
		{"gpt-5", "gpt-5.5", false},
		{"gpt-5", "gpt-5-mini", false},
		{"claude-opus-4", "claude-opus-4-1", false},
		{"glm-5.2", "glm-5.1", false},
		{"glm-5.2", "glm-5.21", false},
		{"glm-5.2", "kimi-for-coding", false},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := Matches(c.expected, c.actual); got != c.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", c.expected, c.actual, got, c.want)
		}
	}
}

func TestExpectationCheck(t *testing.T) {
	e := Expectation{Default: "gpt-6-astra"}
	if e.Check("gpt-5.5-mini") != VerdictMismatch {
		t.Fatal("downgrade must mismatch")
	}
	if e.Check("") != VerdictUnknown {
		t.Fatal("missing model should be unknown by default")
	}
	e.RejectMissing = true
	if e.Check("") != VerdictMismatch {
		t.Fatal("missing model should mismatch when rejected")
	}

	globbed := Expectation{Accept: []string{"gpt-6-astra*"}, Deny: []string{"*mini*"}}
	if globbed.Check("gpt-6-astra-pro") != VerdictAccepted {
		t.Fatal("glob accept failed")
	}
	if globbed.Check("gpt-6-astra-mini") != VerdictMismatch {
		t.Fatal("deny must win over accept")
	}
	alias := Expectation{Accept: []string{"glm-5.2", "glm-5.2-air"}, Default: "my-glm"}
	if alias.Check("glm-5.2-air") != VerdictAccepted || alias.Check("my-glm") != VerdictMismatch {
		t.Fatal("explicit accept list must replace the default")
	}
}

func TestProcessingModel(t *testing.T) {
	cases := map[string]string{
		`{"id":"x","model":"gpt-6-astra","choices":[]}`:                                                          "gpt-6-astra",
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus-5\"}}\n\n": "claude-opus-5",
		"data: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n":                     "gpt-6-astra",
		`{"candidates":[],"modelVersion":"gemini-3-pro"}`:                                                        "gemini-3-pro",
		`[{"candidates":[],"modelVersion":"gemini-3-pro"}]`:                                                      "gemini-3-pro",
		"data: [DONE]\n\n": "",
		`{"choices":[]}`:   "",
	}
	for payload, want := range cases {
		if got := ProcessingModel([]byte(payload)); got != want {
			t.Errorf("ProcessingModel(%q) = %q, want %q", payload, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		msg    string
		want   Kind
	}{
		{429, `{"error":{"type":"insufficient_quota","message":"You exceeded your current quota"}}`, KindQuota},
		{429, "Too Many Requests", KindRateLimit},
		{402, "", KindQuota},
		{403, "usage limit reached for this plan", KindQuota},
		{403, "forbidden", KindAuth},
		{401, "bad key", KindAuth},
		{404, "model_not_found", KindNotFound},
		{500, "boom", KindServer},
		{503, "overloaded", KindServer},
		{400, "prompt is too long", KindClient},
		{400, "model_not_found", KindNotFound},
		{0, "you have hit your usage limit", KindQuota},
		{0, "429 rate limit", KindRateLimit},
		{0, "stream reset by peer", KindServer},
		{0, "上游额度已用完", KindQuota},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.msg); got != c.want {
			t.Errorf("Classify(%d, %q) = %s, want %s", c.status, c.msg, got, c.want)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := map[string]time.Duration{
		`{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`: time.Hour,
		`"retryDelay": "37s"`:                          37 * time.Second,
		"Rate limit reached. Please try again in 20m.": 20 * time.Minute,
		"please retry after 2 hours":                   2 * time.Hour,
		"Please try again in 450ms":                    450 * time.Millisecond,
		"retry after 1h30m":                            90 * time.Minute,
		`{"resets_at": 1800000600}`:                    10 * time.Minute,
		"nothing useful here":                          0,
	}
	for msg, want := range cases {
		if got := RetryAfter(msg, now); got != want {
			t.Errorf("RetryAfter(%q) = %s, want %s", msg, got, want)
		}
	}
}
