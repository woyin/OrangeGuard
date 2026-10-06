package engine

import (
	"errors"
	"testing"
)

type closingHost struct {
	*fakeHost
	closed   int
	failRead bool
}

func (h *closingHost) CloseStream(id string) { h.closed++; h.fakeHost.CloseStream(id) }
func (h *closingHost) ReadStream(id string) (Chunk, error) {
	if h.failRead {
		return Chunk{}, errors.New("injected pre-commit read failure")
	}
	return h.fakeHost.ReadStream(id)
}

type failingSink struct{}

func (failingSink) Emit([]byte) error { return errors.New("client disconnected") }
func TestNestedStreamResources(t *testing.T) {
	for _, mode := range []string{"success", "mismatch", "precommit", "sink-error", "postcommit"} {
		t.Run(mode, func(t *testing.T) {
			scripts := map[string][]outcome{"gpt-a": {{served: "gpt-a"}}, "gpt-b": {{served: "gpt-b"}}, "gpt-c": {{served: "gpt-c"}}}
			if mode == "mismatch" {
				scripts["gpt-a"] = []outcome{{served: "wrong"}}
			}
			if mode == "postcommit" {
				scripts["gpt-a"] = []outcome{{served: "gpt-a", midStreamErr: "reset"}}
			}
			base := newEngine(t, newFakeHost(scripts), nestedConfig)
			h := &closingHost{fakeHost: base.host.(*fakeHost), failRead: mode == "precommit"}
			base.host = h
			var out Sink = &sink{}
			if mode == "sink-error" {
				out = failingSink{}
			}
			err := base.ExecuteStream(req("parent"), out)
			if h.closed != len(h.calls) {
				t.Fatalf("streams leaked: closed=%d calls=%v", h.closed, h.calls)
			}
			if (mode == "sink-error" || mode == "postcommit") && (err == nil || len(h.calls) != 1) {
				t.Fatalf("post-commit retry: err=%v calls=%v", err, h.calls)
			}
			if mode == "precommit" && len(h.calls) != 3 {
				t.Fatalf("pre-commit error did not fail over: %v", h.calls)
			}
		})
	}
}
