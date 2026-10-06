package engine

import (
	"strings"
	"time"

	"github.com/woyin/orangeguard/internal/config"
)

// availability observes group state without advancing a scheduling counter.
// A group's recovery time is that of its earliest eligible descendant.
func (e *Engine) availability(cfg config.Config, model string, path []string) (bool, time.Time) {
	vm, group := cfg.FindVirtual(model)
	if !group {
		until := e.Cooldown.Until(model)
		return until.IsZero(), until
	}
	if len(path) >= config.MaxGroupDepth {
		return false, time.Time{}
	}
	for _, name := range path {
		if strings.EqualFold(name, model) {
			return false, time.Time{}
		}
	}
	path = append(append([]string(nil), path...), model)
	var soonest time.Time
	for _, m := range vm.Members {
		if vm.Strategy == config.StrategyManual && !strings.EqualFold(m.Model, strings.TrimSpace(vm.ManualMember)) {
			continue
		}
		ready, until := e.availability(cfg, m.Model, path)
		if ready {
			return true, time.Time{}
		}
		if !until.IsZero() && (soonest.IsZero() || until.Before(soonest)) {
			soonest = until
		}
	}
	return false, soonest
}
