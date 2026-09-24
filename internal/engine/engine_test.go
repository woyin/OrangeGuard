package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/woyin/orangeguard/internal/config"
)

// outcome scripts one upstream attempt for a model.
type outcome struct {
	served string // processing model reported in the response
	status int    // non-zero: the host call fails with this status
	msg    string
	// stream-only: error delivered as a chunk after the first event
	midStreamErr string
}

type fakeHost struct {
	mu      sync.Mutex
	scripts map[string][]outcome // per model, consumed in order; last one repeats
	calls   []string
	bodies  []string
	streams map[string][][]byte
	nextID  int
	logs    []string
}

func newFakeHost(scripts map[string][]outcome) *fakeHost {
	return &fakeHost{scripts: scripts, streams: map[string][][]byte{}}
}

func (h *fakeHost) next(model string) outcome {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, model)
	script := h.scripts[model]
	if len(script) == 0 {
		return outcome{status: 404, msg: "model_not_found"}
	}
	o := script[0]
	if len(script) > 1 {
		h.scripts[model] = script[1:]
	}
	return o
}

func (h *fakeHost) Execute(_ Request, model string, body []byte) (Response, error) {
	h.mu.Lock()
	h.bodies = append(h.bodies, string(body))
	h.mu.Unlock()
	o := h.next(model)
	if o.status != 0 {
		return Response{}, &UpstreamError{Status: o.status, Message: o.msg}
	}
	return Response{Status: 200, Body: []byte(fmt.Sprintf(`{"model":%q,"choices":[]}`, o.served))}, nil
}

func (h *fakeHost) ExecuteStream(_ Request, model string, _ []byte) (StreamStart, error) {
	o := h.next(model)
	if o.status != 0 {
		return StreamStart{}, &UpstreamError{Status: o.status, Message: o.msg}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := fmt.Sprintf("s%d", h.nextID)
	chunks := [][]byte{
		[]byte(fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":%q}}\n\n", o.served)),
		[]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\n"),
	}
	if o.midStreamErr != "" {
		chunks = append(chunks, []byte("ERR:"+o.midStreamErr))
	}
	h.streams[id] = chunks
	return StreamStart{ID: id, Status: 200}, nil
}

func (h *fakeHost) ReadStream(id string) (Chunk, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	chunks := h.streams[id]
	if len(chunks) == 0 {
		return Chunk{Done: true}, nil
	}
	c := chunks[0]
	h.streams[id] = chunks[1:]
	if s := string(c); strings.HasPrefix(s, "ERR:") {
		return Chunk{Err: strings.TrimPrefix(s, "ERR:")}, nil
	}
	return Chunk{Payload: c}, nil
}

func (h *fakeHost) CloseStream(string) {}

func (h *fakeHost) Log(_, level, message string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, level+" "+message)
}

type sink struct{ out []string }

func (s *sink) Emit(p []byte) error { s.out = append(s.out, string(p)); return nil }

func newEngine(t *testing.T, host *fakeHost, yaml string) *Engine {
	t.Helper()
	cfg, warnings, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected config warnings: %v", warnings)
	}
	e := New(host)
	e.SetConfig(cfg)
	e.sleep = func(time.Duration) {}
	return e
}

func req(model string) Request {
	return Request{Model: model, SourceFormat: "openai", Body: []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"<hi>"}]}`, model))}
}

const guardYAML = `
guard:
  max_retries: 2
  models:
    - model: gpt-6-astra
`

func TestClaims(t *testing.T) {
	e := newEngine(t, newFakeHost(nil), guardYAML+`
virtual_models:
  - name: smart
    members: [{model: a}]
`)
	for model, want := range map[string]bool{"gpt-6-astra": true, "smart": true, "SMART": true, "other": false} {
		if got, _ := e.Claims(model, false); got != want {
			t.Errorf("Claims(%q) = %v, want %v", model, got, want)
		}
	}
	if got, _ := e.Claims("smart", true); got {
		t.Error("bypass header must decline")
	}
}

func TestGuardRetriesUntilCorrectModel(t *testing.T) {
	host := newFakeHost(map[string][]outcome{
		"gpt-6-astra": {{served: "gpt-5.5-mini"}, {served: "gpt-6-astra-2026-08-01"}},
	})
	e := newEngine(t, host, guardYAML)
	resp, f := e.Execute(req("gpt-6-astra"))
	if f != nil {
		t.Fatalf("unexpected failure: %v", f)
	}
	if !strings.Contains(string(resp.Body), "gpt-6-astra-2026-08-01") || len(host.calls) != 2 {
		t.Fatalf("want recovery on 2nd attempt, calls=%v body=%s", host.calls, resp.Body)
	}
}

func TestGuardBlocksPersistentDowngrade(t *testing.T) {
	host := newFakeHost(map[string][]outcome{"gpt-6-astra": {{served: "gpt-5.5-mini"}}})
	e := newEngine(t, host, guardYAML)
	_, f := e.Execute(req("gpt-6-astra"))
	if f == nil || f.Status != http.StatusServiceUnavailable || f.Code != "model_mismatch_blocked" {
		t.Fatalf("want 503 model_mismatch_blocked, got %+v", f)
	}
	if len(host.calls) != 3 {
		t.Fatalf("want 1+2 attempts, got %v", host.calls)
	}
	if e.Cooldown.Available("gpt-6-astra") {
		t.Fatal("a blocked downgrade must put the model in cooldown for virtual models")
	}
}

func TestGuardPassesThroughUpstreamErrors(t *testing.T) {
	host := newFakeHost(map[string][]outcome{"gpt-6-astra": {{status: 429, msg: "insufficient_quota"}}})
	e := newEngine(t, host, guardYAML)
	_, f := e.Execute(req("gpt-6-astra"))
	if f == nil || f.Status != 429 || len(host.calls) != 1 {
		t.Fatalf("want immediate 429 passthrough, got %+v calls=%v", f, host.calls)
	}
}

const virtualYAML = `
guard:
  models:
    - model: a
virtual_models:
  - name: c
    strategy: fallback
    members:
      - model: a
      - model: b
`

func TestVirtualFallbackOnQuotaAndCooldownSkip(t *testing.T) {
	host := newFakeHost(map[string][]outcome{
		"a": {{status: 429, msg: "You exceeded your current quota"}},
		"b": {{served: "b"}},
	})
	e := newEngine(t, host, virtualYAML)
	resp, f := e.Execute(req("c"))
	if f != nil {
		t.Fatalf("unexpected failure %v", f)
	}
	if !strings.Contains(string(resp.Body), `"model":"b"`) {
		t.Fatalf("want response from b, got %s", resp.Body)
	}
	if !strings.Contains(host.bodies[1], `"model":"b"`) || !strings.Contains(host.bodies[1], "<hi>") {
		t.Fatalf("member body not rewritten cleanly: %s", host.bodies[1])
	}
	// Second request must skip the cooling member entirely.
	host.calls = nil
	if _, f := e.Execute(req("c")); f != nil {
		t.Fatal(f)
	}
	if len(host.calls) != 1 || host.calls[0] != "b" {
		t.Fatalf("cooling member not skipped: %v", host.calls)
	}
}

func TestVirtualFailsOverOnDowngrade(t *testing.T) {
	host := newFakeHost(map[string][]outcome{
		"a": {{served: "a-mini"}},
		"b": {{served: "b"}},
	})
	e := newEngine(t, host, virtualYAML)
	if _, f := e.Execute(req("c")); f != nil {
		t.Fatal(f)
	}
	if strings.Join(host.calls, ",") != "a,b" {
		t.Fatalf("calls = %v", host.calls)
	}
	if e.Cooldown.Available("a") {
		t.Fatal("downgrading member must cool down")
	}
}

func TestVirtualClientErrorDoesNotFailOver(t *testing.T) {
	host := newFakeHost(map[string][]outcome{
		"a": {{status: 400, msg: "invalid request: messages required"}},
		"b": {{served: "b"}},
	})
	e := newEngine(t, host, virtualYAML)
	_, f := e.Execute(req("c"))
	if f == nil || f.Status != 400 || len(host.calls) != 1 {
		t.Fatalf("want 400 without failover, got %+v calls=%v", f, host.calls)
	}
	if !e.Cooldown.Available("a") {
		t.Fatal("client errors must not cool a member down")
	}
}

func TestVirtualAllCooling(t *testing.T) {
	host := newFakeHost(map[string][]outcome{
		"a": {{status: 429, msg: "quota"}, {served: "a"}},
		"b": {{status: 429, msg: "quota, try again in 5 seconds"}, {served: "b"}},
	})
	e := newEngine(t, host, virtualYAML)
	_, f := e.Execute(req("c"))
	if f == nil || f.Status != 429 || !strings.Contains(f.Message, "all members") {
		t.Fatalf("want 429 after both members fail, got %+v", f)
	}
	// Default when_all_cooling=soonest: b recovers first, so it is tried first.
	host.calls = nil
	if _, f := e.Execute(req("c")); f != nil {
		t.Fatal(f)
	}
	if host.calls[0] != "b" {
		t.Fatalf("soonest-recovering member should go first: %v", host.calls)
	}

	failYAML := strings.Replace(virtualYAML, "strategy: fallback", "strategy: fallback\n    when_all_cooling: fail", 1)
	e2 := newEngine(t, newFakeHost(nil), failYAML)
	e2.Cooldown = e.Cooldown
	e.Cooldown.RecordFailure("a", "quota", "quota", e.Config().Cooldown)
	e.Cooldown.RecordFailure("b", "quota", "quota", e.Config().Cooldown)
	_, f = e2.Execute(req("c"))
	if f == nil || f.Code != "all_members_cooling_down" || f.Status != 429 {
		t.Fatalf("want all_members_cooling_down, got %+v", f)
	}
}

func TestRoundRobinAndMaxAttempts(t *testing.T) {
	host := newFakeHost(map[string][]outcome{
		"a": {{served: "a"}}, "b": {{served: "b"}}, "x": {{status: 500, msg: "boom"}},
	})
	e := newEngine(t, host, `
virtual_models:
  - name: rr
    strategy: round-robin
    members: [{model: a}, {model: b}]
  - name: capped
    max_attempts: 1
    members: [{model: x}, {model: a}]
`)
	for i := 0; i < 4; i++ {
		if _, f := e.Execute(req("rr")); f != nil {
			t.Fatal(f)
		}
	}
	if got := strings.Join(host.calls, ","); got != "a,b,a,b" {
		t.Fatalf("round robin order = %s", got)
	}
	host.calls = nil
	if _, f := e.Execute(req("capped")); f == nil || len(host.calls) != 1 {
		t.Fatalf("max_attempts not honoured: calls=%v", host.calls)
	}
}

func TestWeightedOrder(t *testing.T) {
	e := newEngine(t, newFakeHost(nil), `
virtual_models:
  - name: w
    strategy: weighted
    members: [{model: a, weight: 1}, {model: b, weight: 3}]
`)
	vm, _ := e.Config().FindVirtual("w")
	e.float = func() float64 { return 0.5 } // 0.5*4 = 2 -> falls in b's range
	if got := e.order(vm); got[0].Model != "b" || got[1].Model != "a" {
		t.Fatalf("weighted order = %v", got)
	}
	e.float = func() float64 { return 0.1 } // 0.4 -> a
	if got := e.order(vm); got[0].Model != "a" {
		t.Fatalf("weighted order = %v", got)
	}
}

func TestStreamGuardAndFailover(t *testing.T) {
	host := newFakeHost(map[string][]outcome{
		"a": {{served: "a-mini"}},
		"b": {{served: "b"}},
	})
	e := newEngine(t, host, virtualYAML)
	s := &sink{}
	if err := e.ExecuteStream(req("c"), s); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(s.out, "")
	if strings.Contains(joined, "a-mini") || !strings.Contains(joined, `"model":"b"`) || len(s.out) != 2 {
		t.Fatalf("client must only see the accepted stream, got %q", joined)
	}
}

func TestStreamErrorBeforeCommitFailsOver(t *testing.T) {
	host := newFakeHost(map[string][]outcome{
		"a": {{status: 429, msg: "rate limit"}},
		"b": {{served: "b"}},
	})
	e := newEngine(t, host, virtualYAML)
	s := &sink{}
	if err := e.ExecuteStream(req("c"), s); err != nil {
		t.Fatal(err)
	}
	if strings.Join(host.calls, ",") != "a,b" || len(s.out) != 2 {
		t.Fatalf("calls=%v out=%d", host.calls, len(s.out))
	}
}

func TestStreamErrorAfterCommitIsReported(t *testing.T) {
	host := newFakeHost(map[string][]outcome{
		"b": {{served: "b", midStreamErr: "upstream reset"}},
	})
	e := newEngine(t, host, `
virtual_models:
  - name: c
    members: [{model: b}, {model: z}]
`)
	s := &sink{}
	err := e.ExecuteStream(req("c"), s)
	if err == nil || len(s.out) != 2 || len(host.calls) != 1 {
		t.Fatalf("want committed stream to end with error and no failover, err=%v calls=%v", err, host.calls)
	}
}

func TestModelRegistrationPublishesCapabilities(t *testing.T) {
	cfg, _, _ := config.Parse([]byte(`
virtual_models:
  - name: smart
    members: [{model: a}]
    capabilities:
      display_name: Smart
      context_length: 400000
      max_output_tokens: 64000
      vision: true
      thinking: {min: 1024, max: 32000, levels: [LOW, High]}
`))
	resp := ModelRegistration(cfg)
	if resp.Provider != "orangeguard" || len(resp.Models) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	m := resp.Models[0]
	if m.ID != "smart" || m.ContextLength != 400000 || m.InputTokenLimit != 400000 || m.MaxCompletionTokens != 64000 ||
		strings.Join(m.SupportedInputModalities, ",") != "text,image" || m.Thinking == nil || m.Thinking.Levels[1] != "high" {
		t.Fatalf("model info = %+v", m)
	}
}

func TestManagement(t *testing.T) {
	e := newEngine(t, newFakeHost(nil), virtualYAML)
	e.Cooldown.RecordFailure("a", "quota", "quota", e.Config().Cooldown)
	status, body := e.HandleManagement("GET", "/v0/management"+StatusPath, nil, nil)
	var report StatusReport
	if status != 200 || json.Unmarshal(body, &report) != nil {
		t.Fatalf("status %d body %s", status, body)
	}
	if report.VirtualModels[0].Members[0].Available || !report.VirtualModels[0].Members[1].Available {
		t.Fatalf("report = %+v", report)
	}
	status, _ = e.HandleManagement("POST", "/v0/management"+ResetPath, nil, []byte(`{"model":"a"}`))
	if status != 200 || !e.Cooldown.Available("a") {
		t.Fatal("reset failed")
	}
}
