package engine

import (
	"strings"
	"sync"
	"testing"
)

func TestNestedChildStrategies(t *testing.T) {
	for _, strategy := range []string{"fallback", "round-robin", "manual", "random", "weighted"} {
		t.Run(strategy, func(t *testing.T) {
			raw := strings.Replace(nestedConfig, "- name: child", "- name: child\n    strategy: "+strategy, 1)
			if strategy == "manual" {
				raw = strings.Replace(raw, "strategy: manual", "strategy: manual\n    manual_member: gpt-b", 1)
			}
			h := newFakeHost(map[string][]outcome{"gpt-a": {{served: "gpt-a"}}, "gpt-b": {{served: "gpt-b"}}})
			e := newEngine(t, h, raw)
			e.shuffle = func(n int, swap func(int, int)) {
				if n > 1 {
					swap(0, n-1)
				}
			}
			e.float = func() float64 { return .99 }
			for i := 0; i < 2; i++ {
				if _, f := e.Execute(req("parent")); f != nil {
					t.Fatal(f)
				}
			}
			want := "gpt-a,gpt-a"
			if strategy == "round-robin" {
				want = "gpt-a,gpt-b"
			}
			if strategy == "manual" || strategy == "random" || strategy == "weighted" {
				want = "gpt-b,gpt-b"
			}
			if got := strings.Join(h.calls, ","); got != want {
				t.Fatalf("child %s: got %s want %s", strategy, got, want)
			}
		})
	}
}

func TestNestedConcurrentRotation(t *testing.T) {
	h := newFakeHost(map[string][]outcome{"gpt-a": {{served: "gpt-a"}}, "gpt-b": {{served: "gpt-b"}}})
	e := newEngine(t, h, strings.Replace(nestedConfig, "- name: child", "- name: child\n    strategy: round-robin", 1))
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, f := e.Execute(req("parent")); f != nil {
				t.Error(f)
			}
		}()
	}
	wg.Wait()
	counts := map[string]int{}
	for _, m := range h.calls {
		counts[m]++
	}
	if counts["gpt-a"] != 20 || counts["gpt-b"] != 20 {
		t.Fatalf("unfair allocation: %v", counts)
	}
}
