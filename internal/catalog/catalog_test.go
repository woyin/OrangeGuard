package catalog

import "testing"

func TestLookup(t *testing.T) {
	r := Lookup("cline-pass/glm-5.3-flash")
	if r.RequestModel != "cline-pass/glm-5.3-flash" || r.Status != "reference_confirmation_required" || len(r.Candidates) == 0 {
		t.Fatalf("unexpected result: %+v", r)
	}
	found := false
	for _, c := range r.Candidates {
		if c.Provider == "zhipuai" {
			found = true
			if c.Capabilities["context_length"] != float64(1000000) || c.Capabilities["max_output_tokens"] != float64(131072) {
				t.Fatalf("unexpected official reference: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("missing zhipuai reference")
	}
	for _, id := range []string{"glm-5.3-flash-not-a-model", "", "glm-5.3-flashx-does-not-exist"} {
		if r := Lookup(id); len(r.Candidates) != 0 || r.Status != "unknown" {
			t.Fatalf("unsafe fuzzy match: %+v", r)
		}
	}
	if len(Lookup("Z.AI/GLM-5.3-FLASH").Candidates) != len(r.Candidates) {
		t.Fatal("case/namespace mismatch")
	}
}
