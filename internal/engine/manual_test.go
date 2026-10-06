package engine

import "testing"

func TestManualNeverSwitchesMember(t *testing.T) {
	for _, stream := range []bool{false, true} {
		host := newFakeHost(map[string][]outcome{"gpt-5": {{served: "gpt-5-mini"}}, "gpt-6": {{served: "gpt-6"}}})
		e := newEngine(t, host, "guard:\n  max_retries: 0\nvirtual_models:\n  - name: manual-test\n    strategy: manual\n    manual_member: gpt-5\n    members:\n      - model: gpt-5\n      - model: gpt-6\n")
		if stream {
			if err := e.ExecuteStream(req("manual-test"), &sink{}); err == nil {
				t.Fatal("mismatch accepted")
			}
		} else {
			if _, f := e.Execute(req("manual-test")); f == nil {
				t.Fatal("mismatch accepted")
			}
		}
		if len(host.calls) != 1 || host.calls[0] != "gpt-5" {
			t.Fatalf("manual switched member: %v", host.calls)
		}
		before := len(host.calls)
		if stream {
			if err := e.ExecuteStream(req("manual-test"), &sink{}); err == nil {
				t.Fatal("cooling selection executed")
			}
		} else {
			if _, f := e.Execute(req("manual-test")); f == nil || f.Code != "manual_member_cooling_down" {
				t.Fatalf("unexpected cooling result: %+v", f)
			}
		}
		if len(host.calls) != before {
			t.Fatal("cooling manual selection made upstream call")
		}
	}
}
