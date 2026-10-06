// Package monitor passively records which model actually served each request,
// from the usage records cpa delivers after every upstream call. It never
// claims, delays or retries a request, so it can watch every model cheaply and
// show where substitution happens before a model is put under guard.
package monitor

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/woyin/orangeguard/internal/detect"
)

// Bounds keep memory flat however many distinct names a deployment sees.
const (
	maxModels       = 1000
	maxServedModels = 8
)

// Recorder aggregates usage per client-facing request name.
type Recorder struct {
	mu     sync.Mutex
	models map[string]*entry
	since  time.Time
	now    func() time.Time
}

type entry struct {
	model        string
	aliases      map[string]struct{}
	executed     map[string]struct{}
	requests     int64
	failed       int64
	reported     int64 // successful responses that named a model
	mismatches   int64
	served       map[string]int64
	lastSeen     time.Time
	lastMismatch time.Time
	lastServed   string
}

// ServedCount is one model name observed in responses.
type ServedCount struct {
	Model string `json:"model"`
	Count int64  `json:"count"`
}

// Stat is the read-only view of one client-facing request name.
type Stat struct {
	Model          string        `json:"model"`
	Aliases        []string      `json:"aliases,omitempty"`
	ExecutedModels []string      `json:"executed_models,omitempty"`
	Requests       int64         `json:"requests"`
	Failed         int64         `json:"failed"`
	Reported       int64         `json:"reported"`
	Mismatches     int64         `json:"mismatches"`
	MismatchRate   float64       `json:"mismatch_rate"`
	Served         []ServedCount `json:"served"`
	LastSeen       time.Time     `json:"last_seen"`
	LastMismatch   time.Time     `json:"last_mismatch,omitzero"`
	LastMismatchAs string        `json:"last_mismatch_as,omitempty"`
}

// New returns an empty recorder. now may be nil to use time.Now.
func New(now func() time.Time) *Recorder {
	if now == nil {
		now = time.Now
	}
	return &Recorder{models: map[string]*entry{}, since: now(), now: now}
}

// Record adds one usage record. model is the upstream model cpa executed,
// alias the client-facing name when one was used, served the model the
// response reported (empty when unknown).
func (r *Recorder) Record(model, alias, served string, failed bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	alias = strings.TrimSpace(alias)
	served = strings.TrimSpace(served)
	requestName := model
	if alias != "" {
		requestName = alias
	}
	key := strings.ToLower(requestName)
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.models[key]
	if e == nil {
		if len(r.models) >= maxModels {
			r.evictOldest()
		}
		e = &entry{model: requestName, aliases: map[string]struct{}{}, executed: map[string]struct{}{}, served: map[string]int64{}}
		r.models[key] = e
	}
	e.requests++
	e.lastSeen = now
	e.executed[model] = struct{}{}
	if alias != "" && !strings.EqualFold(alias, model) {
		e.aliases[alias] = struct{}{}
	}
	if failed {
		e.failed++
		return
	}
	if served == "" {
		return
	}
	e.reported++
	e.addServed(served)
	accept := []string{model}
	if alias != "" {
		accept = append(accept, alias)
	}
	if (detect.Expectation{Accept: accept}).Check(served) == detect.VerdictMismatch {
		e.mismatches++
		e.lastMismatch = now
		e.lastServed = served
	}
}

func (e *entry) addServed(served string) {
	if _, ok := e.served[served]; !ok && len(e.served) >= maxServedModels {
		// Drop the least frequent name to make room.
		var victim string
		var least int64 = -1
		for name, count := range e.served {
			if least < 0 || count < least {
				victim, least = name, count
			}
		}
		delete(e.served, victim)
	}
	e.served[served]++
}

func (r *Recorder) evictOldest() {
	var oldestKey string
	var oldest time.Time
	for key, e := range r.models {
		if oldestKey == "" || e.lastSeen.Before(oldest) {
			oldestKey, oldest = key, e.lastSeen
		}
	}
	delete(r.models, oldestKey)
}

// Snapshot returns all models, those with mismatches first, then by traffic.
func (r *Recorder) Snapshot() []Stat {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Stat, 0, len(r.models))
	for _, e := range r.models {
		st := Stat{
			Model:          e.model,
			Requests:       e.requests,
			Failed:         e.failed,
			Reported:       e.reported,
			Mismatches:     e.mismatches,
			LastSeen:       e.lastSeen,
			LastMismatch:   e.lastMismatch,
			LastMismatchAs: e.lastServed,
			Served:         []ServedCount{},
		}
		if e.reported > 0 {
			st.MismatchRate = float64(e.mismatches) / float64(e.reported)
		}
		for alias := range e.aliases {
			st.Aliases = append(st.Aliases, alias)
		}
		sort.Strings(st.Aliases)
		for model := range e.executed {
			st.ExecutedModels = append(st.ExecutedModels, model)
		}
		sort.Strings(st.ExecutedModels)
		for name, count := range e.served {
			st.Served = append(st.Served, ServedCount{Model: name, Count: count})
		}
		sort.Slice(st.Served, func(i, j int) bool {
			if st.Served[i].Count != st.Served[j].Count {
				return st.Served[i].Count > st.Served[j].Count
			}
			return st.Served[i].Model < st.Served[j].Model
		})
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Mismatches > 0) != (out[j].Mismatches > 0) {
			return out[i].Mismatches > 0
		}
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// Since reports when recording started (process start or last reset).
func (r *Recorder) Since() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.since
}

// Reset clears all statistics.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models = map[string]*entry{}
	r.since = r.now()
}
