package config

import (
	"fmt"
	"strings"
)

const MaxGroupDepth = 8

func validateGroups(cfg Config) error {
	groups := map[string]VirtualModel{}
	for _, g := range cfg.VirtualModels {
		groups[strings.ToLower(strings.TrimSpace(g.Name))] = g
	}
	// Reuse only visits with at least as much ancestor depth: a shared DAG
	// must still be checked when reached through a longer path.
	checkedDepth := map[string]int{}
	var visit func(string, []string) error
	visit = func(name string, path []string) error {
		for _, ancestor := range path {
			if ancestor == name {
				return fmt.Errorf("orangeguard: group cycle: %s", strings.Join(append(path, name), " -> "))
			}
		}
		if depth, ok := checkedDepth[name]; ok && depth >= len(path) {
			return nil
		}
		path = append(path, name)
		if len(path) > MaxGroupDepth {
			return fmt.Errorf("orangeguard: group depth exceeds %d: %s", MaxGroupDepth, strings.Join(path, " -> "))
		}
		for _, m := range groups[name].Members {
			child := strings.ToLower(strings.TrimSpace(m.Model))
			if _, ok := groups[child]; !ok {
				continue
			}
			if len(m.Expect) > 0 || len(m.Deny) > 0 || m.MaxRetries != 0 {
				return fmt.Errorf("orangeguard: group %q reference %q cannot have leaf expect/deny/max_retries", name, m.Model)
			}
			if err := visit(child, path); err != nil {
				return err
			}
		}
		checkedDepth[name] = len(path) - 1
		return nil
	}
	for name := range groups {
		if err := visit(name, nil); err != nil {
			return err
		}
	}
	return nil
}
