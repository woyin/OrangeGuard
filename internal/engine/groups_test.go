package engine

import (
	"github.com/woyin/orangeguard/internal/detect"
	"testing"
)

const nestedConfig = `guard:
  max_retries: 0
virtual_models:
  - name: parent
    members:
      - model: child
      - model: gpt-c
  - name: child
    members:
      - model: gpt-a
      - model: gpt-b
`

func TestNestedAttemptsStayInChild(t *testing.T) {
	for _, stream := range []bool{false, true} {
		host := newFakeHost(map[string][]outcome{"gpt-a": {{served: "wrong"}}, "gpt-b": {{served: "gpt-b"}}, "gpt-c": {{served: "gpt-c"}}})
		e := newEngine(t, host, nestedConfig)
		if stream {
			if err := e.ExecuteStream(req("parent"), &sink{}); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, f := e.Execute(req("parent")); f != nil {
				t.Fatal(f)
			}
		}
		if len(host.calls) != 2 || host.calls[0] != "gpt-a" || host.calls[1] != "gpt-b" {
			t.Fatalf("child not executed atomically: %v", host.calls)
		}
	}
}

func TestNestedStreamCommitStopsParentFailover(t *testing.T) {
	host := newFakeHost(map[string][]outcome{"gpt-a": {{served: "gpt-a", midStreamErr: "upstream reset"}}})
	e := newEngine(t, host, nestedConfig)
	s := &sink{}
	if err := e.ExecuteStream(req("parent"), s); err == nil || len(host.calls) != 1 || len(s.out) == 0 {
		t.Fatalf("committed child incorrectly failed over: err=%v calls=%v output=%v", err, host.calls, s.out)
	}
}

func TestNestedCooldownSkipsUnavailableSubtree(t *testing.T) {
	host := newFakeHost(map[string][]outcome{"gpt-c": {{served: "gpt-c"}}})
	e := newEngine(t, host, nestedConfig)
	e.Cooldown.RecordFailure("gpt-a", detect.KindMismatch, "test", e.Config().Cooldown)
	e.Cooldown.RecordFailure("gpt-b", detect.KindMismatch, "test", e.Config().Cooldown)
	if _, f := e.Execute(req("parent")); f != nil {
		t.Fatal(f)
	}
	if len(host.calls) != 1 || host.calls[0] != "gpt-c" {
		t.Fatalf("unavailable subtree executed: %v", host.calls)
	}
}

func TestManualParentKeepsChildFallback(t *testing.T) {
	host := newFakeHost(map[string][]outcome{"gpt-a": {{served: "wrong"}}, "gpt-b": {{served: "gpt-b"}}})
	e := newEngine(t, host, nestedConfig+"  - name: pinned\n    strategy: manual\n    manual_member: child\n    members:\n      - model: child\n      - model: gpt-c\n")
	if _, f := e.Execute(req("pinned")); f != nil {
		t.Fatal(f)
	}
	if len(host.calls) != 2 || host.calls[1] != "gpt-b" {
		t.Fatalf("manual parent changed child strategy: %v", host.calls)
	}
}

func TestNestedAncestorBudget(t *testing.T) {
	host := newFakeHost(map[string][]outcome{"gpt-a": {{served: "wrong"}}, "gpt-b": {{served: "gpt-b"}}})
	e := newEngine(t, host, nestedConfig+"  - name: limited\n    max_attempts: 1\n    members:\n      - model: child\n      - model: gpt-b\n")
	_, f := e.Execute(req("limited"))
	if f == nil || f.Code != "attempt_budget_exhausted" || len(host.calls) != 1 {
		t.Fatalf("ancestor budget failed: %+v calls=%v", f, host.calls)
	}
}
