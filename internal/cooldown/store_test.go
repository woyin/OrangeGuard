package cooldown

import (
	"testing"
	"time"

	"github.com/woyin/orangeguard/internal/config"
	"github.com/woyin/orangeguard/internal/detect"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestQuotaCooldownAndBackoff(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(c.now)
	cfg := config.Default().Cooldown

	if d := s.RecordFailure("A", detect.KindQuota, "quota exceeded", cfg); d != 30*time.Minute {
		t.Fatalf("first quota cooldown = %s", d)
	}
	if s.Available("a") {
		t.Fatal("model must be cooling (case-insensitive)")
	}
	c.t = c.t.Add(31 * time.Minute)
	if !s.Available("A") {
		t.Fatal("cooldown must expire")
	}
	if d := s.RecordFailure("A", detect.KindQuota, "quota exceeded", cfg); d != time.Hour {
		t.Fatalf("second consecutive quota cooldown should double, got %s", d)
	}
	s.RecordSuccess("A")
	if !s.Available("A") {
		t.Fatal("success clears cooldown")
	}
	if d := s.RecordFailure("A", detect.KindQuota, "quota exceeded", cfg); d != 30*time.Minute {
		t.Fatalf("strikes must reset after success, got %s", d)
	}
}

func TestRetryAfterHintAndCap(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(c.now)
	cfg := config.Default().Cooldown
	if d := s.RecordFailure("A", detect.KindRateLimit, "try again in 5 seconds", cfg); d != 5*time.Second {
		t.Fatalf("hint not honoured: %s", d)
	}
	if d := s.RecordFailure("B", detect.KindQuota, `"resets_in_seconds": 999999`, cfg); d != 6*time.Hour {
		t.Fatalf("cap not applied: %s", d)
	}
}

func TestHostCooldownHintOnlyExtends(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(c.now)
	cfg := config.Default().Cooldown
	msg := `{"error":{"code":"model_cooldown","reset_seconds":1,"last_upstream_error":"insufficient_quota"}}`
	if kind := detect.Classify(429, msg); kind != detect.KindQuota {
		t.Fatalf("cpa model_cooldown wrapping a quota error should classify as quota, got %s", kind)
	}
	if d := s.RecordFailure("A", detect.KindQuota, msg, cfg); d != 30*time.Minute {
		t.Fatalf("cpa's 1s credential backoff must not shorten the quota cooldown, got %s", d)
	}
	long := `{"error":{"code":"model_cooldown","reset_seconds":7200}}`
	if d := s.RecordFailure("B", detect.KindQuota, long, cfg); d != 2*time.Hour {
		t.Fatalf("a longer cpa cooldown should extend ours, got %s", d)
	}
}

func TestLongerCooldownWins(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(c.now)
	cfg := config.Default().Cooldown
	s.RecordFailure("A", detect.KindQuota, "quota", cfg)
	s.RecordFailure("A", detect.KindRateLimit, "slow down", cfg)
	if until := s.Until("A"); until.Sub(c.t) != 30*time.Minute {
		t.Fatalf("rate limit must not shorten quota cooldown: %s", until.Sub(c.t))
	}
	if st := s.Snapshot(); len(st) != 1 || st[0].Reason != string(detect.KindQuota) {
		t.Fatalf("snapshot: %+v", st)
	}
}

func TestServerErrorThreshold(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(c.now)
	cfg := config.Default().Cooldown // threshold 2
	if d := s.RecordFailure("A", detect.KindServer, "502", cfg); d != 0 || !s.Available("A") {
		t.Fatal("first server error must be tolerated")
	}
	if d := s.RecordFailure("A", detect.KindServer, "502", cfg); d != 30*time.Second {
		t.Fatalf("second server error should cool down 30s, got %s", d)
	}
	if d := s.RecordFailure("B", detect.KindClient, "bad request", cfg); d != 0 || !s.Available("B") {
		t.Fatal("client errors never cool down")
	}
}

func TestDisabledAndReset(t *testing.T) {
	s := New(nil)
	cfg := config.Default().Cooldown
	off := false
	cfg.Enabled = &off
	if d := s.RecordFailure("A", detect.KindQuota, "quota", cfg); d != 0 || !s.Available("A") {
		t.Fatal("disabled cooldown must not block")
	}
	cfg.Enabled = nil
	s.RecordFailure("A", detect.KindQuota, "quota", cfg)
	s.RecordFailure("B", detect.KindQuota, "quota", cfg)
	if n := s.Reset("a"); n != 1 || !s.Available("A") || s.Available("B") {
		t.Fatal("single reset failed")
	}
	if n := s.Reset(""); n != 1 || !s.Available("B") {
		t.Fatal("reset all failed")
	}
}
