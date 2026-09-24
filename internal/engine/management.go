package engine

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/woyin/orangeguard/internal/cooldown"
)

// Management API paths, registered under /v0/management and protected by the
// cpa management key.
const (
	StatusPath = "/plugins/orangeguard/status"
	ResetPath  = "/plugins/orangeguard/cooldown/reset"
)

// VirtualStatus summarises one virtual model and the live state of its members.
type VirtualStatus struct {
	Name     string         `json:"name"`
	Strategy string         `json:"strategy"`
	Members  []MemberStatus `json:"members"`
}

// MemberStatus is one member as seen by the cooldown table.
type MemberStatus struct {
	Model            string `json:"model"`
	Weight           int    `json:"weight,omitempty"`
	Available        bool   `json:"available"`
	RemainingSeconds int64  `json:"remaining_seconds,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

// StatusReport is the body of GET /v0/management/plugins/orangeguard/status.
type StatusReport struct {
	Enabled       bool              `json:"enabled"`
	GuardedModels []string          `json:"guarded_models"`
	VirtualModels []VirtualStatus   `json:"virtual_models"`
	Cooldowns     []cooldown.Status `json:"cooldowns"`
}

// Status builds a live report of configuration and cooldown state.
func (e *Engine) Status() StatusReport {
	cfg := e.Config()
	snapshot := e.Cooldown.Snapshot()
	byModel := map[string]cooldown.Status{}
	for _, st := range snapshot {
		byModel[strings.ToLower(st.Model)] = st
	}
	report := StatusReport{Enabled: cfg.Enabled, Cooldowns: snapshot, GuardedModels: []string{}, VirtualModels: []VirtualStatus{}}
	for _, rule := range cfg.Guard.Models {
		report.GuardedModels = append(report.GuardedModels, rule.Model)
	}
	for _, vm := range cfg.VirtualModels {
		vs := VirtualStatus{Name: vm.Name, Strategy: vm.Strategy}
		for _, m := range vm.Members {
			ms := MemberStatus{Model: m.Model, Available: true}
			if vm.Strategy == "weighted" {
				ms.Weight = m.Weight
			}
			if st, ok := byModel[strings.ToLower(m.Model)]; ok && st.CoolingDown {
				ms.Available = false
				ms.RemainingSeconds = st.RemainingSeconds
				ms.Reason = st.Reason
			}
			vs.Members = append(vs.Members, ms)
		}
		report.VirtualModels = append(report.VirtualModels, vs)
	}
	return report
}

// HandleManagement serves the plugin's management routes. It returns the
// status code and JSON body.
func (e *Engine) HandleManagement(method, path string, query map[string][]string, body []byte) (int, []byte) {
	path = strings.TrimRight(path, "/")
	switch {
	case strings.HasSuffix(path, StatusPath) && method == http.MethodGet:
		return jsonBody(http.StatusOK, e.Status())
	case strings.HasSuffix(path, ResetPath) && method == http.MethodPost:
		var req struct {
			Model string `json:"model"`
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			if errDecode := json.Unmarshal(body, &req); errDecode != nil {
				return jsonBody(http.StatusBadRequest, map[string]string{"error": "body must be JSON like {\"model\":\"name\"} or empty to reset all"})
			}
		}
		if req.Model == "" && len(query["model"]) > 0 {
			req.Model = query["model"][0]
		}
		cleared := e.Cooldown.Reset(req.Model)
		return jsonBody(http.StatusOK, map[string]any{"cleared": cleared, "model": req.Model})
	default:
		return jsonBody(http.StatusNotFound, map[string]string{"error": "unknown orangeguard management route"})
	}
}

func jsonBody(status int, v any) (int, []byte) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return http.StatusInternalServerError, []byte(`{"error":"marshal failed"}`)
	}
	return status, raw
}
