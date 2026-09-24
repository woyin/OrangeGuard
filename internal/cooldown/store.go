// Package cooldown tracks upstream models that recently failed so virtual
// models can skip them instead of hammering an exhausted quota.
package cooldown

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/woyin/orangeguard/internal/config"
	"github.com/woyin/orangeguard/internal/detect"
)

// Store is an in-memory, concurrency-safe cooldown table keyed by upstream
// model name (case-insensitive). State is intentionally not persisted: after
// a restart every model gets one fresh chance, which is cheap.
type Store struct {
	mu      sync.Mutex
	entries map[string]*entry
	now     func() time.Time
}

type entry struct {
	model       string
	until       time.Time
	kind        detect.Kind
	lastError   string
	strikes     map[detect.Kind]int // consecutive failures per kind, reset on success
	failures    int64
	successes   int64
	lastFailure time.Time
	lastSuccess time.Time
}

// Status is a read-only snapshot of one model's state.
type Status struct {
	Model            string    `json:"model"`
	CoolingDown      bool      `json:"cooling_down"`
	Until            time.Time `json:"until,omitzero"`
	RemainingSeconds int64     `json:"remaining_seconds,omitempty"`
	Reason           string    `json:"reason,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	Failures         int64     `json:"failures"`
	Successes        int64     `json:"successes"`
	LastFailure      time.Time `json:"last_failure,omitzero"`
	LastSuccess      time.Time `json:"last_success,omitzero"`
}

// New returns an empty store. now may be nil to use time.Now.
func New(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{entries: map[string]*entry{}, now: now}
}

func key(model string) string { return strings.ToLower(strings.TrimSpace(model)) }

// Now returns the store's clock.
func (s *Store) Now() time.Time { return s.now() }

// Until returns when model becomes available again; zero means available now.
func (s *Store) Until(model string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[key(model)]
	if e == nil || !e.until.After(s.now()) {
		return time.Time{}
	}
	return e.until
}

// Available reports whether model is not cooling down.
func (s *Store) Available(model string) bool { return s.Until(model).IsZero() }

// RecordSuccess clears the cooldown and every strike for model.
func (s *Store) RecordSuccess(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.get(model)
	e.until = time.Time{}
	e.kind = ""
	e.strikes = nil
	e.successes++
	e.lastSuccess = s.now()
}

// RecordFailure registers a failure and returns the cooldown applied (0 when
// the failure did not start one, for example the first server error below the
// threshold or a client error).
func (s *Store) RecordFailure(model string, kind detect.Kind, message string, cfg config.CooldownConfig) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	e := s.get(model)
	e.failures++
	e.lastFailure = now
	e.lastError = truncate(message, 300)
	if !cfg.On() || kind == detect.KindClient {
		return 0
	}
	if e.strikes == nil {
		e.strikes = map[detect.Kind]int{}
	}
	e.strikes[kind]++
	strikes := e.strikes[kind]

	base := baseFor(kind, cfg)
	if kind == detect.KindServer {
		if strikes < cfg.ServerErrorThreshold {
			return 0
		}
		strikes = strikes - cfg.ServerErrorThreshold + 1
	}
	d := time.Duration(float64(base) * math.Pow(cfg.BackoffMultiplier, float64(strikes-1)))
	if cfg.UseRetryAfter() && (kind == detect.KindQuota || kind == detect.KindRateLimit) {
		if hint := detect.RetryAfter(message, now); hint > 0 {
			// A hint from cpa's own credential cooldown may only extend ours.
			if !detect.IsHostCooldown(message) || hint > d {
				d = hint
			}
		}
	}
	if limit := time.Duration(cfg.MaxSeconds) * time.Second; d > limit {
		d = limit
	}
	if d <= 0 {
		return 0
	}
	// Never shorten an existing, longer cooldown (e.g. a quota cooldown must
	// not be replaced by a later 60s rate-limit one).
	if next := now.Add(d); next.After(e.until) {
		e.until = next
		e.kind = kind
	}
	return e.until.Sub(now)
}

// Reset clears one model (or every model when model is empty). It returns the
// number of entries cleared.
func (s *Store) Reset(model string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(model) == "" {
		n := len(s.entries)
		s.entries = map[string]*entry{}
		return n
	}
	if _, ok := s.entries[key(model)]; ok {
		delete(s.entries, key(model))
		return 1
	}
	return 0
}

// Snapshot returns every tracked model sorted by name.
func (s *Store) Snapshot() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]Status, 0, len(s.entries))
	for _, e := range s.entries {
		st := Status{
			Model:       e.model,
			LastError:   e.lastError,
			Failures:    e.failures,
			Successes:   e.successes,
			LastFailure: e.lastFailure,
			LastSuccess: e.lastSuccess,
		}
		if e.until.After(now) {
			st.CoolingDown = true
			st.Until = e.until
			st.RemainingSeconds = int64(math.Ceil(e.until.Sub(now).Seconds()))
			st.Reason = string(e.kind)
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

func (s *Store) get(model string) *entry {
	k := key(model)
	e := s.entries[k]
	if e == nil {
		e = &entry{model: strings.TrimSpace(model)}
		s.entries[k] = e
	}
	return e
}

func baseFor(kind detect.Kind, cfg config.CooldownConfig) time.Duration {
	var secs int
	switch kind {
	case detect.KindQuota:
		secs = cfg.QuotaSeconds
	case detect.KindRateLimit:
		secs = cfg.RateLimitSeconds
	case detect.KindAuth:
		secs = cfg.AuthSeconds
	case detect.KindNotFound:
		secs = cfg.NotFoundSeconds
	case detect.KindServer:
		secs = cfg.ServerErrorSeconds
	case detect.KindMismatch:
		secs = cfg.MismatchSeconds
	}
	return time.Duration(secs) * time.Second
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
