// Package engine implements orangeguard's routing and execution logic
// independently of the cgo plugin glue, so it can be tested with a fake host.
package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/woyin/orangeguard/internal/config"
	"github.com/woyin/orangeguard/internal/cooldown"
	"github.com/woyin/orangeguard/internal/detect"
)

// maxConcurrentExecutions is a circuit breaker: if the host ever failed to
// skip this plugin's router for nested executions, the router starts
// declining instead of recursing without bound.
const maxConcurrentExecutions = 256

// Request is the client request the executor was asked to run.
type Request struct {
	CallbackID   string
	SourceFormat string
	Format       string
	Model        string
	Alt          string
	Body         []byte
	Headers      http.Header
	Query        url.Values
}

// Response is a completed non-streaming upstream response.
type Response struct {
	Status  int
	Headers http.Header
	Body    []byte
}

// StreamStart describes an opened upstream stream.
type StreamStart struct {
	ID      string
	Status  int
	Headers http.Header
}

// Chunk is one read from an upstream stream.
type Chunk struct {
	Payload []byte
	Err     string
	Done    bool
}

// UpstreamError is a failed host model execution carrying the HTTP status the
// host would have returned to a client.
type UpstreamError struct {
	Status  int
	Code    string
	Message string
}

func (e *UpstreamError) Error() string { return e.Message }

// Host is the subset of cpa host callbacks the engine needs.
type Host interface {
	Execute(req Request, model string, body []byte) (Response, error)
	ExecuteStream(req Request, model string, body []byte) (StreamStart, error)
	ReadStream(id string) (Chunk, error)
	CloseStream(id string)
	Log(callbackID, level, message string)
}

// Sink receives the chunks delivered to the client.
type Sink interface {
	Emit(payload []byte) error
}

// Failure is the error returned to the client when no attempt succeeded.
type Failure struct {
	Status  int
	Code    string
	Message string
}

func (f *Failure) Error() string { return f.Message }

// Engine holds configuration and shared cooldown state.
type Engine struct {
	host     Host
	cfg      atomic.Pointer[config.Config]
	Cooldown *cooldown.Store
	counters sync.Map // virtual model name -> *atomic.Uint64
	active   atomic.Int64

	// Overridable for tests.
	sleep   func(time.Duration)
	shuffle func(n int, swap func(i, j int))
	float   func() float64
}

// New creates an engine with default configuration.
func New(host Host) *Engine {
	e := &Engine{
		host:     host,
		Cooldown: cooldown.New(nil),
		sleep:    time.Sleep,
		shuffle:  rand.Shuffle,
		float:    rand.Float64,
	}
	cfg := config.Default()
	e.cfg.Store(&cfg)
	return e
}

// SetConfig atomically replaces the configuration. Cooldown state survives.
func (e *Engine) SetConfig(cfg config.Config) { e.cfg.Store(&cfg) }

// Config returns the current configuration.
func (e *Engine) Config() config.Config { return *e.cfg.Load() }

// Claims reports whether the router should send model to this plugin's
// executor, with a reason for logs.
func (e *Engine) Claims(model string, bypass bool) (bool, string) {
	cfg := e.Config()
	if !cfg.Enabled || bypass || e.active.Load() >= maxConcurrentExecutions {
		return false, ""
	}
	if _, ok := cfg.FindVirtual(model); ok {
		return true, "orangeguard_virtual_model"
	}
	if _, ok := cfg.FindGuard(model); ok {
		return true, "orangeguard_guarded_model"
	}
	return false, ""
}

// target is one upstream model the plan may try.
type target struct {
	model  string
	expect *detect.Expectation
	tries  int // 1 + same-model retries on mismatch
}

type plan struct {
	requested      string
	virtual        bool
	targets        []target
	maxAttempts    int
	failoverClient bool
	retryDelay     time.Duration
	skipped        []string
}

func (e *Engine) buildPlan(cfg config.Config, requested string) (plan, *Failure) {
	p := plan{requested: requested, retryDelay: time.Duration(cfg.Guard.RetryDelayMs) * time.Millisecond}

	vm, isVirtual := cfg.FindVirtual(requested)
	if !isVirtual {
		rule, guarded := cfg.FindGuard(requested)
		t := target{model: requested, tries: 1}
		if guarded {
			t.expect = expectationFromRule(rule, requested)
			t.tries = 1 + cfg.RetryBudget(rule)
		}
		p.targets = []target{t}
		p.maxAttempts = t.tries
		return p, nil
	}

	p.virtual = true
	p.failoverClient = vm.FailoverOnClientError
	ordered := e.order(vm)
	var available, cooling []target
	coolingUntil := map[string]time.Time{}
	for _, m := range ordered {
		t := target{model: m.Model, tries: 1 + m.MaxRetries}
		switch {
		case len(m.Expect) > 0 || len(m.Deny) > 0:
			t.expect = &detect.Expectation{
				Accept:        m.Expect,
				Deny:          m.Deny,
				Default:       m.Model,
				RejectMissing: cfg.Guard.OnMissingModel == config.MissingModelReject,
			}
		default:
			if rule, ok := cfg.FindGuard(m.Model); ok {
				t.expect = expectationFromRule(rule, m.Model)
			} else if vm.Guard {
				t.expect = &detect.Expectation{
					Default:       m.Model,
					RejectMissing: cfg.Guard.OnMissingModel == config.MissingModelReject,
				}
			}
		}
		if until := e.Cooldown.Until(m.Model); !until.IsZero() {
			coolingUntil[strings.ToLower(m.Model)] = until
			cooling = append(cooling, t)
			p.skipped = append(p.skipped, m.Model)
			continue
		}
		available = append(available, t)
	}

	if len(available) == 0 {
		sort.SliceStable(cooling, func(i, j int) bool {
			return coolingUntil[strings.ToLower(cooling[i].model)].Before(coolingUntil[strings.ToLower(cooling[j].model)])
		})
		soonest := coolingUntil[strings.ToLower(cooling[0].model)]
		wait := max(time.Until(soonest), 0).Round(time.Second)
		if vm.WhenAllCooling == config.AllCoolingFail {
			return p, &Failure{
				Status:  http.StatusTooManyRequests,
				Code:    "all_members_cooling_down",
				Message: fmt.Sprintf("orangeguard: every member of %q is cooling down; next one (%s) is available in %s", vm.Name, cooling[0].model, wait),
			}
		}
		// Degrade gracefully: try members in order of whichever recovers first.
		available = cooling
		p.skipped = nil
	}

	p.targets = available
	for _, t := range available {
		p.maxAttempts += t.tries
	}
	if vm.MaxAttempts > 0 && vm.MaxAttempts < p.maxAttempts {
		p.maxAttempts = vm.MaxAttempts
	}
	return p, nil
}

func expectationFromRule(rule config.GuardRule, model string) *detect.Expectation {
	return &detect.Expectation{
		Accept:        rule.Expect,
		Deny:          rule.Deny,
		Default:       model,
		RejectMissing: rule.OnMissingModel == config.MissingModelReject,
	}
}

// order returns the members in the order the strategy wants to try them.
func (e *Engine) order(vm config.VirtualModel) []config.Member {
	members := append([]config.Member(nil), vm.Members...)
	n := len(members)
	switch vm.Strategy {
	case config.StrategyRoundRobin:
		counter, _ := e.counters.LoadOrStore(strings.ToLower(vm.Name), new(atomic.Uint64))
		start := int((counter.(*atomic.Uint64).Add(1) - 1) % uint64(n))
		return append(members[start:], members[:start]...)
	case config.StrategyRandom:
		e.shuffle(n, func(i, j int) { members[i], members[j] = members[j], members[i] })
		return members
	case config.StrategyWeighted:
		// Weighted sampling without replacement: the first pick follows the
		// weights, later picks are the fallback order.
		out := make([]config.Member, 0, n)
		for len(members) > 0 {
			total := 0
			for _, m := range members {
				total += m.Weight
			}
			r := e.float() * float64(total)
			idx := len(members) - 1
			for i, m := range members {
				r -= float64(m.Weight)
				if r < 0 {
					idx = i
					break
				}
			}
			out = append(out, members[idx])
			members = append(members[:idx], members[idx+1:]...)
		}
		return out
	default:
		return members
	}
}

// Execute runs a non-streaming request through the plan.
func (e *Engine) Execute(req Request) (Response, *Failure) {
	e.active.Add(1)
	defer e.active.Add(-1)

	cfg := e.Config()
	p, failure := e.buildPlan(cfg, req.Model)
	if failure != nil {
		e.logf(req.CallbackID, "warn", "all members cooling down | requested=%s", req.Model)
		return Response{}, failure
	}
	e.logSkipped(req, p)

	attempts := 0
	var last *Failure
	for _, t := range p.targets {
		body := bodyFor(req.Body, req.Model, t.model)
		mismatch := ""
		for try := 0; try < t.tries && attempts < p.maxAttempts; try++ {
			if try > 0 && p.retryDelay > 0 {
				e.sleep(p.retryDelay)
			}
			attempts++
			resp, errExec := e.host.Execute(req, t.model, body)
			if errExec == nil && resp.Status >= http.StatusBadRequest {
				errExec = &UpstreamError{Status: resp.Status, Message: string(resp.Body)}
			}
			if errExec != nil {
				f, kind := e.recordError(req, cfg, t.model, errExec, attempts)
				// Upstream errors on a directly requested model are passed
				// through untouched; only virtual models fail over.
				if !p.virtual || !kind.Failover(p.failoverClient) {
					return Response{}, f
				}
				last = f
				mismatch = ""
				break
			}
			if t.expect != nil {
				actual := detect.ProcessingModel(resp.Body)
				if t.expect.Check(actual) == detect.VerdictMismatch {
					mismatch = orUnknown(actual)
					e.logf(req.CallbackID, "warn", "model mismatch detected | requested=%s upstream=%s served=%s attempt=%d/%d transport=non-stream",
						req.Model, t.model, mismatch, try+1, t.tries)
					continue
				}
			}
			e.Cooldown.RecordSuccess(t.model)
			if attempts > 1 {
				e.logf(req.CallbackID, "info", "request recovered | requested=%s served_by=%s attempts=%d transport=non-stream", req.Model, t.model, attempts)
			}
			return resp, nil
		}
		if mismatch != "" {
			last = e.recordMismatch(req, cfg, t.model, mismatch)
		}
		if attempts >= p.maxAttempts {
			break
		}
	}
	return Response{}, e.finalFailure(req, p, last, attempts)
}

// streamOutcome classifies one streaming attempt.
type streamOutcome int

const (
	streamDelivered streamOutcome = iota // committed and finished (possibly with a post-commit error)
	streamMismatch                       // discarded before commit: wrong model
	streamFailed                         // discarded before commit: upstream error
)

// Probe limits: the processing model arrives in the first event, so these
// only stop the plugin buffering a whole response that never names a model.
const (
	maxProbeChunks = 32
	maxProbeBytes  = 256 * 1024
)

// ExecuteStream runs a streaming request through the plan. Nothing reaches
// the sink until an attempt is accepted, so failover and guard retries are
// invisible to the client. The returned error (if any) terminates the client
// stream.
func (e *Engine) ExecuteStream(req Request, sink Sink) error {
	e.active.Add(1)
	defer e.active.Add(-1)

	cfg := e.Config()
	p, failure := e.buildPlan(cfg, req.Model)
	if failure != nil {
		e.logf(req.CallbackID, "warn", "all members cooling down | requested=%s", req.Model)
		return failure
	}
	e.logSkipped(req, p)

	attempts := 0
	var last *Failure
	for _, t := range p.targets {
		body := bodyFor(req.Body, req.Model, t.model)
		mismatch := ""
	tries:
		for try := 0; try < t.tries && attempts < p.maxAttempts; try++ {
			if try > 0 && p.retryDelay > 0 {
				e.sleep(p.retryDelay)
			}
			attempts++
			outcome, actual, errAttempt := e.streamOnce(req, t, body, sink)
			switch outcome {
			case streamDelivered:
				if errAttempt != nil {
					// Already committed: the client has partial output, so the
					// only honest option is to end the stream with the error.
					var ue *UpstreamError
					status := 0
					if errors.As(errAttempt, &ue) {
						status = ue.Status
					}
					kind := detect.Classify(status, errAttempt.Error())
					e.Cooldown.RecordFailure(t.model, kind, errAttempt.Error(), cfg.Cooldown)
					e.logf(req.CallbackID, "warn", "stream failed after commit | requested=%s upstream=%s kind=%s error=%q",
						req.Model, t.model, kind, truncate(errAttempt.Error(), 200))
					return errAttempt
				}
				e.Cooldown.RecordSuccess(t.model)
				if attempts > 1 {
					e.logf(req.CallbackID, "info", "request recovered | requested=%s served_by=%s attempts=%d transport=stream", req.Model, t.model, attempts)
				}
				return nil
			case streamMismatch:
				mismatch = orUnknown(actual)
				e.logf(req.CallbackID, "warn", "model mismatch detected | requested=%s upstream=%s served=%s attempt=%d/%d transport=stream",
					req.Model, t.model, mismatch, try+1, t.tries)
				continue
			case streamFailed:
				f, kind := e.recordError(req, cfg, t.model, errAttempt, attempts)
				if !p.virtual || !kind.Failover(p.failoverClient) {
					return f
				}
				last = f
				mismatch = ""
				break tries
			}
		}
		if mismatch != "" {
			last = e.recordMismatch(req, cfg, t.model, mismatch)
		}
		if attempts >= p.maxAttempts {
			break
		}
	}
	return e.finalFailure(req, p, last, attempts)
}

// streamOnce runs one upstream stream attempt.
func (e *Engine) streamOnce(req Request, t target, body []byte, sink Sink) (streamOutcome, string, error) {
	start, errStart := e.host.ExecuteStream(req, t.model, body)
	if errStart != nil {
		return streamFailed, "", errStart
	}
	if strings.TrimSpace(start.ID) == "" {
		return streamFailed, "", &UpstreamError{Status: http.StatusBadGateway, Message: "host model stream returned an empty stream_id"}
	}
	defer e.host.CloseStream(start.ID)

	if start.Status >= http.StatusBadRequest {
		// Collect the error body so it can be classified and, if this is the
		// last resort, still explained to the client.
		var buf bytes.Buffer
		for buf.Len() < maxProbeBytes {
			chunk, errRead := e.host.ReadStream(start.ID)
			if errRead != nil || chunk.Err != "" {
				break
			}
			buf.Write(chunk.Payload)
			if chunk.Done {
				break
			}
		}
		return streamFailed, "", &UpstreamError{Status: start.Status, Message: strings.TrimSpace(buf.String())}
	}

	committed := false
	var buffered [][]byte
	var probe bytes.Buffer
	actual := ""
	commit := func() error {
		committed = true
		for _, payload := range buffered {
			if errEmit := sink.Emit(payload); errEmit != nil {
				return errEmit
			}
		}
		buffered = nil
		return nil
	}

	for {
		chunk, errRead := e.host.ReadStream(start.ID)
		if errRead != nil {
			if committed {
				return streamDelivered, actual, errRead
			}
			return streamFailed, "", errRead
		}
		if chunk.Err != "" {
			errChunk := &UpstreamError{Message: chunk.Err}
			if committed {
				return streamDelivered, actual, errChunk
			}
			return streamFailed, "", errChunk
		}
		if len(chunk.Payload) > 0 {
			payload := bytes.Clone(chunk.Payload)
			if committed {
				if errEmit := sink.Emit(payload); errEmit != nil {
					return streamDelivered, actual, errEmit
				}
			} else {
				buffered = append(buffered, payload)
				if t.expect == nil {
					if errCommit := commit(); errCommit != nil {
						return streamDelivered, actual, errCommit
					}
				} else {
					probe.Write(payload)
					actual = detect.ProcessingModel(probe.Bytes())
					switch {
					case actual != "":
						if t.expect.Check(actual) == detect.VerdictMismatch {
							return streamMismatch, actual, nil
						}
						if errCommit := commit(); errCommit != nil {
							return streamDelivered, actual, errCommit
						}
					case len(buffered) >= maxProbeChunks || probe.Len() >= maxProbeBytes:
						if t.expect.RejectMissing {
							return streamMismatch, "", nil
						}
						if errCommit := commit(); errCommit != nil {
							return streamDelivered, actual, errCommit
						}
					}
				}
			}
		}
		if chunk.Done {
			break
		}
	}
	if !committed {
		if t.expect != nil && actual == "" && t.expect.RejectMissing {
			return streamMismatch, "", nil
		}
		if errCommit := commit(); errCommit != nil {
			return streamDelivered, actual, errCommit
		}
	}
	return streamDelivered, actual, nil
}

// recordError classifies a failed attempt, updates the cooldown table and
// returns the client-facing failure.
func (e *Engine) recordError(req Request, cfg config.Config, model string, err error, attempt int) (*Failure, detect.Kind) {
	status := 0
	code := "upstream_error"
	var ue *UpstreamError
	if errors.As(err, &ue) {
		status = ue.Status
		if ue.Code != "" {
			code = ue.Code
		}
	}
	msg := err.Error()
	kind := detect.Classify(status, msg)
	cd := e.Cooldown.RecordFailure(model, kind, msg, cfg.Cooldown)
	e.logf(req.CallbackID, "warn", "upstream attempt failed | requested=%s upstream=%s status=%d kind=%s cooldown=%s attempt=%d error=%q",
		req.Model, model, status, kind, cd.Round(time.Second), attempt, truncate(msg, 200))
	if status == 0 {
		status = statusForKind(kind)
	}
	return &Failure{Status: status, Code: code, Message: msg}, kind
}

func (e *Engine) recordMismatch(req Request, cfg config.Config, model, actual string) *Failure {
	msg := fmt.Sprintf("orangeguard: %q was served by %q; refusing to return a substituted model", model, actual)
	cd := e.Cooldown.RecordFailure(model, detect.KindMismatch, msg, cfg.Cooldown)
	e.logf(req.CallbackID, "error", "model mismatch blocked | requested=%s upstream=%s served=%s cooldown=%s",
		req.Model, model, actual, cd.Round(time.Second))
	return &Failure{Status: http.StatusServiceUnavailable, Code: "model_mismatch_blocked", Message: msg}
}

func (e *Engine) finalFailure(req Request, p plan, last *Failure, attempts int) *Failure {
	if last == nil {
		last = &Failure{
			Status:  http.StatusServiceUnavailable,
			Code:    "no_upstream_available",
			Message: fmt.Sprintf("orangeguard: no upstream could serve %q", req.Model),
		}
	}
	if p.virtual {
		e.logf(req.CallbackID, "error", "virtual model exhausted | requested=%s attempts=%d last_error=%q", req.Model, attempts, truncate(last.Message, 200))
		last = &Failure{
			Status:  last.Status,
			Code:    last.Code,
			Message: fmt.Sprintf("orangeguard: all members of %q failed after %d attempt(s); last error: %s", req.Model, attempts, last.Message),
		}
	}
	return last
}

func (e *Engine) logSkipped(req Request, p plan) {
	if len(p.skipped) > 0 {
		e.logf(req.CallbackID, "debug", "skipping cooling members | requested=%s skipped=%s", req.Model, strings.Join(p.skipped, ","))
	}
}

func (e *Engine) logf(callbackID, level, format string, args ...any) {
	if e.host == nil {
		return
	}
	e.host.Log(callbackID, level, "orangeguard: "+fmt.Sprintf(format, args...))
}

func statusForKind(kind detect.Kind) int {
	switch kind {
	case detect.KindQuota, detect.KindRateLimit:
		return http.StatusTooManyRequests
	case detect.KindAuth:
		return http.StatusForbidden
	case detect.KindNotFound:
		return http.StatusNotFound
	case detect.KindClient:
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}

// bodyFor rewrites the top-level "model" field when a virtual model is
// executed through one of its members. The host routes by the explicit model
// argument, but keeping the body consistent avoids surprises in translators
// and request logs.
func bodyFor(body []byte, requested, model string) []byte {
	if strings.EqualFold(requested, model) || len(body) == 0 {
		return body
	}
	var fields map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &fields); errUnmarshal != nil {
		return body
	}
	if _, ok := fields["model"]; !ok {
		return body
	}
	encodedModel, _ := json.Marshal(model)
	fields["model"] = encodedModel
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if errEncode := enc.Encode(fields); errEncode != nil {
		return body
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// EstimateTokens returns a rough input token count for count_tokens calls on
// claimed models (the host offers no count-tokens callback to delegate to).
func EstimateTokens(body []byte) int {
	return max(1, len(body)/4)
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "<unreported>"
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
