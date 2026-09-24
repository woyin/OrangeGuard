package config

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

const sample = `
enabled: true
guard:
  max_retries: 2
  models:
    - model: gpt-6-astra
      expect: gpt-6-astra
      deny: ["*mini*"]
    - model: "claude-*"
cooldown:
  quota_seconds: 3600
virtual_models:
  - name: smart
    strategy: rr
    members:
      - model: gpt-6-astra
      - model: claude-opus-5
        weight: 0
      - model: smart
      - model: gpt-6-astra
    capabilities:
      context_length: 200000
      vision: true
  - name: nested
    members:
      - model: smart
  - name: ""
`

func TestParse(t *testing.T) {
	cfg, warnings, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 {
		t.Fatal("expected warnings for self-reference, duplicate, nesting and empty name")
	}
	if cfg.Provider != DefaultProvider || cfg.Cooldown.QuotaSeconds != 3600 || cfg.Cooldown.RateLimitSeconds != 60 {
		t.Fatalf("defaults not merged: %+v", cfg.Cooldown)
	}
	if len(cfg.VirtualModels) != 1 {
		t.Fatalf("want 1 virtual model, got %d", len(cfg.VirtualModels))
	}
	vm := cfg.VirtualModels[0]
	if vm.Strategy != StrategyRoundRobin || len(vm.Members) != 2 || vm.Members[1].Weight != 1 {
		t.Fatalf("unexpected virtual model: %+v", vm)
	}
	if got := vm.Capabilities.InputModalities; len(got) != 2 || got[1] != "image" {
		t.Fatalf("vision shorthand not applied: %v", got)
	}
	rule, ok := cfg.FindGuard("gpt-6-astra")
	if !ok || len(rule.Expect) != 1 || cfg.RetryBudget(rule) != 2 {
		t.Fatalf("guard rule not parsed: %+v", rule)
	}
	if _, ok := cfg.FindGuard("claude-opus-5"); !ok {
		t.Fatal("glob guard rule should match")
	}
	if _, ok := cfg.FindVirtual("SMART"); !ok {
		t.Fatal("virtual lookup must be case-insensitive")
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"*", "anything/at-all", true},
		{"gpt-6*", "gpt-6-astra", true},
		{"*mini*", "gpt-5.5-mini", true},
		{"*mini*", "gpt-6-astra", false},
		{"claude-?-opus", "claude-4-opus", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
	}
	for _, c := range cases {
		if got := Glob(c.p, c.s); got != c.want {
			t.Errorf("Glob(%q, %q) = %v, want %v", c.p, c.s, got, c.want)
		}
	}
}

// TestExampleConfig keeps config.example.yaml valid and warning-free.
func TestExampleConfig(t *testing.T) {
	raw, errRead := os.ReadFile("../../config.example.yaml")
	if errRead != nil {
		t.Fatal(errRead)
	}
	var doc struct {
		Plugins struct {
			Configs map[string]yaml.Node `yaml:"configs"`
		} `yaml:"plugins"`
	}
	if errDecode := yaml.Unmarshal(raw, &doc); errDecode != nil {
		t.Fatal(errDecode)
	}
	node, ok := doc.Plugins.Configs["orangeguard"]
	if !ok {
		t.Fatal("plugins.configs.orangeguard missing")
	}
	block, _ := yaml.Marshal(&node)
	cfg, warnings, errParse := Parse(block)
	if errParse != nil || len(warnings) > 0 {
		t.Fatalf("example config: err=%v warnings=%v", errParse, warnings)
	}
	if len(cfg.VirtualModels) != 2 || len(cfg.Guard.Models) != 3 {
		t.Fatalf("example config parsed unexpectedly: %d virtual, %d guard", len(cfg.VirtualModels), len(cfg.Guard.Models))
	}
}
