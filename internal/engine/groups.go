package engine

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/woyin/orangeguard/internal/config"
)

type attemptBudget struct{ remaining int }
type executionState struct {
	cfg       config.Config
	root      string
	remaining int
	committed bool
}

func (e *Engine) prepareExecution(req Request) (Request, *Failure) {
	if req.execution == nil {
		req.execution = &executionState{cfg: e.Config(), root: req.Model, remaining: 64}
	}
	if vm, ok := req.execution.cfg.FindVirtual(req.Model); ok {
		for _, name := range req.groupPath {
			if strings.EqualFold(name, vm.Name) {
				return req, &Failure{Status: 400, Code: "group_cycle", Message: "orangeguard: runtime group cycle"}
			}
		}
		req.groupPath = append(append([]string(nil), req.groupPath...), vm.Name)
		if len(req.groupPath) > config.MaxGroupDepth {
			return req, &Failure{Status: 400, Code: "group_depth_exceeded", Message: "orangeguard: runtime group depth exceeded"}
		}
		req.inheritedGuard = req.inheritedGuard || vm.Guard
		if vm.MaxAttempts > 0 {
			req.budgets = append(append([]*attemptBudget(nil), req.budgets...), &attemptBudget{remaining: vm.MaxAttempts})
		}
	}
	return req, nil
}

func (req Request) consumeAttempt() *Failure {
	if req.execution.remaining <= 0 {
		return &Failure{Status: http.StatusBadGateway, Code: "attempt_budget_exhausted", Message: "orangeguard: request safety budget exhausted (64 leaf calls)"}
	}
	for _, budget := range req.budgets {
		if budget.remaining <= 0 {
			return &Failure{Status: http.StatusBadGateway, Code: "attempt_budget_exhausted", Message: fmt.Sprintf("orangeguard: ancestor attempt budget exhausted at %s", strings.Join(req.groupPath, " -> "))}
		}
	}
	req.execution.remaining--
	for _, budget := range req.budgets {
		budget.remaining--
	}
	return nil
}
func terminalGroupFailure(f *Failure) bool {
	return f.Code == "attempt_budget_exhausted" || f.Code == "group_cycle" || f.Code == "group_depth_exceeded" || f.Code == "request_cancelled"
}
