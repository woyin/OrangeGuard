package engine

import "testing"

func TestDefaultNamespacedGuard(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, c := range []struct {
			model, served string
			blocked       bool
		}{
			{"cline-pass/deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash", false},
			{"cline-pass/deepseek-v4.1-pro", "deepseek/deepseek-v4.1-flash", true},
			{"openai/gpt-6.1-sol", "gpt-6.1-sol", false},
			{"openai/gpt-5", "gpt-5-mini", true},
			{"z-ai/glm-4.5", "glm-4.5-air", true},
		} {
			host := newFakeHost(map[string][]outcome{c.model: {{served: c.served}}})
			e := newEngine(t, host, "guard:\n  max_retries: 0\n")
			if claimed, _ := e.Claims(c.model, false); !claimed {
				t.Fatalf("default guard did not claim %q", c.model)
			}
			var blocked bool
			if stream {
				s := &sink{}
				blocked = e.ExecuteStream(req(c.model), s) != nil
				if c.blocked && len(s.out) != 0 {
					t.Fatalf("mismatched stream leaked to client: %v", s.out)
				}
			} else {
				_, f := e.Execute(req(c.model))
				blocked = f != nil
				if f != nil && f.Code != "model_mismatch_blocked" {
					t.Fatalf("unexpected failure: %+v", f)
				}
			}
			if len(host.calls) != 1 || host.calls[0] != c.model {
				t.Fatalf("original routing name lost: want %q, calls=%v", c.model, host.calls)
			}
			if blocked != c.blocked || len(host.calls) != 1 {
				t.Errorf("stream=%v %q -> %q: blocked=%v calls=%v", stream, c.model, c.served, blocked, host.calls)
			}
			if e.Cooldown.Available(c.model) == c.blocked {
				t.Errorf("stream=%v unexpected cooldown for %q", stream, c.model)
			}
		}
	}
}
