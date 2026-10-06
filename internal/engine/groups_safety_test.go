package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/woyin/orangeguard/internal/config"
)

func TestSharedDAGSafetyBudget(t *testing.T) {
	for _, stream := range []bool{false, true} {
		var b strings.Builder
		b.WriteString("guard:\n  models: []\nvirtual_models:\n")
		scripts := map[string][]outcome{}
		for i := 0; i < 65; i++ {
			model := fmt.Sprintf("leaf-%d", i)
			scripts[model] = []outcome{{status: 500, msg: "injected"}}
		}
		b.WriteString("  - name: root\n    members:\n")
		for i := 0; i < 65; i++ {
			fmt.Fprintf(&b, "      - model: branch-%d\n", i)
		}
		for i := 0; i < 65; i++ {
			fmt.Fprintf(&b, "  - name: branch-%d\n    members: [{model: shared}]\n", i)
		}
		b.WriteString("  - name: shared\n    strategy: round-robin\n    max_attempts: 0\n    members:\n")
		// Ordinary failures exhaust the shared subtree, so the safety ceiling is
		// exercised within its traversal without relying on cooldown semantics.
		for i := 0; i < 65; i++ {
			fmt.Fprintf(&b, "      - model: leaf-%d\n", i)
		}
		h := newFakeHost(scripts)
		e := newEngine(t, h, b.String())
		var f *Failure
		if stream {
			err := e.ExecuteStream(req("root"), &sink{})
			f, _ = err.(*Failure)
		} else {
			_, f = e.Execute(req("root"))
		}
		if f == nil || f.Code != "attempt_budget_exhausted" || len(h.calls) != 64 {
			t.Fatalf("safety cap: failure=%+v calls=%d", f, len(h.calls))
		}
	}
}

func TestAncestorGuardAndLeafDeny(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			raw := strings.Replace(nestedConfig, "- name: parent", "- name: parent\n    guard: true", 1)
			raw = strings.Replace(raw, "guard:\n  max_retries: 0", "guard:\n  models: []\n  max_retries: 0", 1)
			if explicit {
				raw = strings.Replace(raw, "- model: gpt-a", "- model: gpt-a\n        expect: ['*']\n        deny: ['*mini*']", 1)
			}
			h := newFakeHost(map[string][]outcome{"gpt-a": {{served: "gpt-a-mini"}}, "gpt-b": {{served: "gpt-b"}}})
			e := newEngine(t, h, raw)
			if stream {
				if err := e.ExecuteStream(req("parent"), &sink{}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, f := e.Execute(req("parent")); f != nil {
					t.Fatal(f)
				}
			}
			if strings.Join(h.calls, ",") != "gpt-a,gpt-b" {
				t.Fatalf("ancestor guard or explicit deny bypassed: %v", h.calls)
			}
		}
	}
}

func TestRuntimeRejectsUnsafeGraph(t *testing.T) {
	for _, stream := range []bool{false, true} {
		e := New(newFakeHost(nil))
		cfg := config.Default()
		cfg.VirtualModels = []config.VirtualModel{{Name: "a", Members: []config.Member{{Model: "a"}}, Strategy: config.StrategyFallback}}
		e.SetConfig(cfg)
		var f *Failure
		if stream {
			err := e.ExecuteStream(req("a"), &sink{})
			f, _ = err.(*Failure)
		} else {
			_, f = e.Execute(req("a"))
		}
		if f == nil || f.Code != "group_cycle" {
			t.Fatalf("runtime cycle not rejected: %+v", f)
		}
	}
}
