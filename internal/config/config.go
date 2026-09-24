// Package config parses and validates the orangeguard plugin configuration
// (the YAML block under plugins.configs.orangeguard in the cpa config).
package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Strategy names accepted for virtual models.
const (
	StrategyFallback   = "fallback"
	StrategyRoundRobin = "round-robin"
	StrategyRandom     = "random"
	StrategyWeighted   = "weighted"
)

// Behaviours when every member of a virtual model is cooling down.
const (
	AllCoolingSoonest = "soonest"
	AllCoolingFail    = "fail"
)

// Behaviours when a response does not report a processing model.
const (
	MissingModelAccept = "accept"
	MissingModelReject = "reject"
)

// DefaultProvider is the provider id virtual models are registered under.
const DefaultProvider = "orangeguard"

// StringList accepts either a YAML scalar or a sequence of scalars.
type StringList []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *StringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var value string
		if errDecode := node.Decode(&value); errDecode != nil {
			return errDecode
		}
		*s = StringList{value}
		return nil
	case yaml.SequenceNode:
		var values []string
		if errDecode := node.Decode(&values); errDecode != nil {
			return errDecode
		}
		*s = StringList(values)
		return nil
	default:
		return fmt.Errorf("expected a string or a list of strings")
	}
}

// Config is the full plugin configuration.
type Config struct {
	Enabled bool `yaml:"enabled"`
	// Provider is the provider id the virtual models are registered under.
	Provider      string         `yaml:"provider"`
	Guard         GuardConfig    `yaml:"guard"`
	Cooldown      CooldownConfig `yaml:"cooldown"`
	VirtualModels []VirtualModel `yaml:"virtual_models"`
}

// GuardConfig configures upstream model-substitution detection.
type GuardConfig struct {
	// MaxRetries is the default number of extra attempts on the same model
	// after a substitution is detected on a directly requested guarded model.
	MaxRetries int `yaml:"max_retries"`
	// RetryDelayMs is the pause between those attempts.
	RetryDelayMs int `yaml:"retry_delay_ms"`
	// OnMissingModel decides what happens when a response names no model.
	OnMissingModel string      `yaml:"on_missing_model"`
	Models         []GuardRule `yaml:"models"`
}

// GuardRule protects one client-facing model name (glob patterns allowed).
type GuardRule struct {
	Model string `yaml:"model"`
	// Expect lists the processing-model names (globs allowed) the upstream may
	// report. When empty the requested model name itself is expected.
	Expect StringList `yaml:"expect"`
	// Deny lists processing-model names (globs allowed) that are always rejected.
	Deny           StringList `yaml:"deny"`
	MaxRetries     *int       `yaml:"max_retries"`
	OnMissingModel string     `yaml:"on_missing_model"`
}

// CooldownConfig controls how long a failing upstream model is skipped by
// virtual models. All durations are in seconds.
type CooldownConfig struct {
	Enabled            *bool `yaml:"enabled"`
	QuotaSeconds       int   `yaml:"quota_seconds"`
	RateLimitSeconds   int   `yaml:"rate_limit_seconds"`
	AuthSeconds        int   `yaml:"auth_seconds"`
	NotFoundSeconds    int   `yaml:"not_found_seconds"`
	ServerErrorSeconds int   `yaml:"server_error_seconds"`
	MismatchSeconds    int   `yaml:"mismatch_seconds"`
	// ServerErrorThreshold is how many consecutive server errors are tolerated
	// before a server-error cooldown starts.
	ServerErrorThreshold int `yaml:"server_error_threshold"`
	// BackoffMultiplier grows the cooldown on consecutive failures of the same kind.
	BackoffMultiplier float64 `yaml:"backoff_multiplier"`
	MaxSeconds        int     `yaml:"max_seconds"`
	// HonorRetryAfter uses a reset hint found in the upstream error (e.g.
	// "retry after 120s", "resets_in_seconds": 3600) instead of the default.
	HonorRetryAfter *bool `yaml:"honor_retry_after"`
}

// On reports whether cooldown tracking is active.
func (c CooldownConfig) On() bool { return c.Enabled == nil || *c.Enabled }

// UseRetryAfter reports whether upstream reset hints are honoured.
func (c CooldownConfig) UseRetryAfter() bool { return c.HonorRetryAfter == nil || *c.HonorRetryAfter }

// VirtualModel merges several upstream models under one client-facing name.
type VirtualModel struct {
	Name     string   `yaml:"name"`
	Strategy string   `yaml:"strategy"`
	Members  []Member `yaml:"members"`
	// MaxAttempts caps the number of upstream attempts per request. 0 means
	// every member may be tried once (plus its own guard retries).
	MaxAttempts int `yaml:"max_attempts"`
	// WhenAllCooling decides what happens when every member is cooling down.
	WhenAllCooling string `yaml:"when_all_cooling"`
	// FailoverOnClientError also fails over on 400-class errors that are not
	// quota/auth/not-found related (e.g. a context-length error on a smaller
	// member). Off by default because the same request usually fails everywhere.
	FailoverOnClientError bool `yaml:"failover_on_client_error"`
	// Guard checks every member's processing model even without a guard rule.
	Guard        bool         `yaml:"guard"`
	Capabilities Capabilities `yaml:"capabilities"`
}

// Member is one upstream model inside a virtual model.
type Member struct {
	Model  string     `yaml:"model"`
	Weight int        `yaml:"weight"`
	Expect StringList `yaml:"expect"`
	Deny   StringList `yaml:"deny"`
	// MaxRetries is the number of extra attempts on this member after a
	// substitution is detected before failing over. Defaults to 0.
	MaxRetries int `yaml:"max_retries"`
}

// Capabilities is the metadata published for a virtual model so clients and
// agents can discover its context window, modalities and reasoning controls.
type Capabilities struct {
	DisplayName         string     `yaml:"display_name"`
	Description         string     `yaml:"description"`
	OwnedBy             string     `yaml:"owned_by"`
	Type                string     `yaml:"type"`
	ContextLength       int64      `yaml:"context_length"`
	InputTokenLimit     int64      `yaml:"input_token_limit"`
	MaxOutputTokens     int64      `yaml:"max_output_tokens"`
	InputModalities     StringList `yaml:"input_modalities"`
	OutputModalities    StringList `yaml:"output_modalities"`
	Vision              bool       `yaml:"vision"`
	SupportedParameters StringList `yaml:"supported_parameters"`
	GenerationMethods   StringList `yaml:"generation_methods"`
	Thinking            *Thinking  `yaml:"thinking"`
}

// Thinking describes reasoning budget controls.
type Thinking struct {
	Min            int        `yaml:"min"`
	Max            int        `yaml:"max"`
	ZeroAllowed    bool       `yaml:"zero_allowed"`
	DynamicAllowed bool       `yaml:"dynamic_allowed"`
	Levels         StringList `yaml:"levels"`
}

// Default returns the configuration used when the plugin block is empty.
func Default() Config {
	return Config{
		Enabled:  true,
		Provider: DefaultProvider,
		Guard: GuardConfig{
			MaxRetries:     3,
			RetryDelayMs:   200,
			OnMissingModel: MissingModelAccept,
		},
		Cooldown: CooldownConfig{
			QuotaSeconds:         1800,
			RateLimitSeconds:     60,
			AuthSeconds:          600,
			NotFoundSeconds:      3600,
			ServerErrorSeconds:   30,
			MismatchSeconds:      600,
			ServerErrorThreshold: 2,
			BackoffMultiplier:    2,
			MaxSeconds:           6 * 3600,
		},
	}
}

// Parse decodes raw YAML on top of the defaults and validates it.
// Invalid entries are dropped and reported as warnings rather than failing the
// whole plugin, so one typo does not disable every other rule.
func Parse(raw []byte) (Config, []string, error) {
	cfg := Default()
	if len(strings.TrimSpace(string(raw))) > 0 {
		if errDecode := yaml.Unmarshal(raw, &cfg); errDecode != nil {
			return Config{}, nil, fmt.Errorf("orangeguard: decode config: %w", errDecode)
		}
	}
	warnings := cfg.normalize()
	return cfg, warnings, nil
}

func (c *Config) normalize() []string {
	var warnings []string
	warn := func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }

	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "" {
		c.Provider = DefaultProvider
	}

	g := &c.Guard
	g.MaxRetries = max(g.MaxRetries, 0)
	g.RetryDelayMs = max(g.RetryDelayMs, 0)
	g.OnMissingModel = normalizeMissing(g.OnMissingModel, MissingModelAccept)
	rules := make([]GuardRule, 0, len(g.Models))
	for i, rule := range g.Models {
		rule.Model = strings.TrimSpace(rule.Model)
		if rule.Model == "" {
			warn("guard.models[%d]: model is empty, rule ignored", i)
			continue
		}
		rule.Expect = cleanList(rule.Expect)
		rule.Deny = cleanList(rule.Deny)
		if rule.MaxRetries != nil && *rule.MaxRetries < 0 {
			zero := 0
			rule.MaxRetries = &zero
		}
		rule.OnMissingModel = normalizeMissing(rule.OnMissingModel, g.OnMissingModel)
		rules = append(rules, rule)
	}
	g.Models = rules

	cd := &c.Cooldown
	cd.QuotaSeconds = max(cd.QuotaSeconds, 0)
	cd.RateLimitSeconds = max(cd.RateLimitSeconds, 0)
	cd.AuthSeconds = max(cd.AuthSeconds, 0)
	cd.NotFoundSeconds = max(cd.NotFoundSeconds, 0)
	cd.ServerErrorSeconds = max(cd.ServerErrorSeconds, 0)
	cd.MismatchSeconds = max(cd.MismatchSeconds, 0)
	cd.ServerErrorThreshold = max(cd.ServerErrorThreshold, 1)
	if cd.BackoffMultiplier < 1 {
		cd.BackoffMultiplier = 1
	}
	if cd.MaxSeconds <= 0 {
		cd.MaxSeconds = 6 * 3600
	}

	seen := map[string]bool{}
	virtuals := make([]VirtualModel, 0, len(c.VirtualModels))
	for i, vm := range c.VirtualModels {
		vm.Name = strings.TrimSpace(vm.Name)
		if vm.Name == "" {
			warn("virtual_models[%d]: name is empty, entry ignored", i)
			continue
		}
		key := strings.ToLower(vm.Name)
		if seen[key] {
			warn("virtual_models[%d]: duplicate name %q, entry ignored", i, vm.Name)
			continue
		}
		vm.Strategy = normalizeStrategy(vm.Strategy)
		if vm.Strategy == "" {
			warn("virtual_models[%d] %q: unknown strategy, using fallback", i, vm.Name)
			vm.Strategy = StrategyFallback
		}
		switch strings.ToLower(strings.TrimSpace(vm.WhenAllCooling)) {
		case AllCoolingFail:
			vm.WhenAllCooling = AllCoolingFail
		default:
			vm.WhenAllCooling = AllCoolingSoonest
		}
		vm.MaxAttempts = max(vm.MaxAttempts, 0)
		members := make([]Member, 0, len(vm.Members))
		memberSeen := map[string]bool{}
		for j, m := range vm.Members {
			m.Model = strings.TrimSpace(m.Model)
			if m.Model == "" {
				warn("virtual_models[%d] %q: members[%d] has no model, ignored", i, vm.Name, j)
				continue
			}
			if strings.EqualFold(m.Model, vm.Name) {
				warn("virtual_models[%d] %q: member cannot reference the virtual model itself, ignored", i, vm.Name)
				continue
			}
			if memberSeen[strings.ToLower(m.Model)] {
				warn("virtual_models[%d] %q: duplicate member %q, ignored", i, vm.Name, m.Model)
				continue
			}
			memberSeen[strings.ToLower(m.Model)] = true
			if m.Weight <= 0 {
				m.Weight = 1
			}
			m.MaxRetries = max(m.MaxRetries, 0)
			m.Expect = cleanList(m.Expect)
			m.Deny = cleanList(m.Deny)
			members = append(members, m)
		}
		if len(members) == 0 {
			warn("virtual_models[%d] %q: no usable members, entry ignored", i, vm.Name)
			continue
		}
		vm.Members = members
		vm.Capabilities.normalize()
		seen[key] = true
		virtuals = append(virtuals, vm)
	}
	// Members are executed through the host with this plugin's router skipped,
	// so a member naming another virtual model could never be resolved.
	for i := range virtuals {
		kept := virtuals[i].Members[:0]
		for _, m := range virtuals[i].Members {
			if seen[strings.ToLower(m.Model)] {
				warn("virtual model %q: member %q is itself a virtual model; nesting is not supported, ignored", virtuals[i].Name, m.Model)
				continue
			}
			kept = append(kept, m)
		}
		virtuals[i].Members = kept
	}
	final := virtuals[:0]
	for _, vm := range virtuals {
		if len(vm.Members) == 0 {
			warn("virtual model %q: no usable members left, entry ignored", vm.Name)
			continue
		}
		final = append(final, vm)
	}
	c.VirtualModels = final
	return warnings
}

func (caps *Capabilities) normalize() {
	caps.InputModalities = lowerList(caps.InputModalities)
	caps.OutputModalities = lowerList(caps.OutputModalities)
	if len(caps.InputModalities) == 0 {
		caps.InputModalities = StringList{"text"}
	}
	if caps.Vision && !contains(caps.InputModalities, "image") {
		caps.InputModalities = append(caps.InputModalities, "image")
	}
	if len(caps.OutputModalities) == 0 {
		caps.OutputModalities = StringList{"text"}
	}
	caps.SupportedParameters = cleanList(caps.SupportedParameters)
	caps.GenerationMethods = cleanList(caps.GenerationMethods)
	if caps.Thinking != nil {
		caps.Thinking.Levels = lowerList(caps.Thinking.Levels)
	}
}

// FindVirtual returns the virtual model named name (case-insensitive).
func (c Config) FindVirtual(name string) (VirtualModel, bool) {
	name = strings.TrimSpace(name)
	for _, vm := range c.VirtualModels {
		if strings.EqualFold(vm.Name, name) {
			return vm, true
		}
	}
	return VirtualModel{}, false
}

// FindGuard returns the first guard rule whose model pattern matches name.
func (c Config) FindGuard(name string) (GuardRule, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return GuardRule{}, false
	}
	for _, rule := range c.Guard.Models {
		if Glob(rule.Model, name) {
			return rule, true
		}
	}
	return GuardRule{}, false
}

// RetryBudget returns the number of extra same-model attempts for a rule.
func (c Config) RetryBudget(rule GuardRule) int {
	if rule.MaxRetries != nil {
		return *rule.MaxRetries
	}
	return c.Guard.MaxRetries
}

func normalizeStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "fallback", "failover", "priority", "ordered":
		return StrategyFallback
	case "round-robin", "round_robin", "roundrobin", "rr":
		return StrategyRoundRobin
	case "random":
		return StrategyRandom
	case "weighted", "weighted-random", "weighted_random":
		return StrategyWeighted
	default:
		return ""
	}
}

func normalizeMissing(s, def string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case MissingModelAccept:
		return MissingModelAccept
	case MissingModelReject:
		return MissingModelReject
	default:
		return def
	}
}

func cleanList(in StringList) StringList {
	out := make(StringList, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func lowerList(in StringList) StringList {
	out := cleanList(in)
	for i := range out {
		out[i] = strings.ToLower(out[i])
	}
	return out
}

func contains(list StringList, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// Glob reports whether name matches pattern case-insensitively. `*` matches any
// run of characters (including '/'), `?` matches one character.
func Glob(pattern, name string) bool {
	p := []rune(strings.ToLower(strings.TrimSpace(pattern)))
	n := []rune(strings.ToLower(strings.TrimSpace(name)))
	// Iterative wildcard matching with single-star backtracking.
	pi, ni, star, mark := 0, 0, -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == n[ni]):
			pi++
			ni++
		case pi < len(p) && p[pi] == '*':
			star = pi
			mark = ni
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			ni = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// HasWildcard reports whether pattern contains glob metacharacters.
func HasWildcard(pattern string) bool {
	return strings.ContainsAny(pattern, "*?")
}
