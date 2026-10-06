package config

import "testing"

func TestManualSelectionValidation(t *testing.T) {
	for _, selection := range []string{"", "other"} {
		_, _, err := Parse([]byte("virtual_models:\n  - name: group\n    strategy: manual\n    manual_member: '" + selection + "'\n    members:\n      - model: openai/gpt-6\n"))
		if err == nil {
			t.Fatalf("invalid manual selection %q accepted", selection)
		}
	}
	cfg, _, err := Parse([]byte("virtual_models:\n  - name: group\n    strategy: manual\n    manual_member: OPENAI/GPT-6\n    members:\n      - model: openai/gpt-6\n"))
	if err != nil || len(cfg.VirtualModels) != 1 {
		t.Fatalf("valid selection rejected: %v", err)
	}
}
