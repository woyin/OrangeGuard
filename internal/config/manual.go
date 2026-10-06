package config

import (
	"fmt"
	"strings"
)

func validateManual(cfg Config) error {
	for _, vm := range cfg.VirtualModels {
		if normalizeStrategy(vm.Strategy) != StrategyManual {
			continue
		}
		selected := strings.TrimSpace(vm.ManualMember)
		if selected == "" {
			return fmt.Errorf("orangeguard: manual group %q requires manual_member", vm.Name)
		}
		found := false
		for _, m := range vm.Members {
			if strings.EqualFold(strings.TrimSpace(m.Model), selected) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("orangeguard: manual group %q selected member %q is not a direct member", vm.Name, selected)
		}
	}
	return nil
}
