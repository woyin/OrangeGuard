// Package catalog provides refreshable reference capabilities, not channel guarantees.
package catalog

import (
	"embed"
	"sort"
	"strings"
	"sync"
)

//go:embed snapshot.json
var files embed.FS

type Candidate struct {
	Provider     string         `json:"provider"`
	ModelID      string         `json:"model_id"`
	Name         string         `json:"name"`
	Capabilities map[string]any `json:"capabilities"`
	Reasoning    bool           `json:"reasoning"`
	Updated      string         `json:"updated"`
}
type Result struct {
	RequestModel string      `json:"request_model"`
	Source       string      `json:"source"`
	SnapshotDate string      `json:"snapshot_date"`
	Status       string      `json:"status"`
	Candidates   []Candidate `json:"candidates"`
}

var once sync.Once
var dataMu sync.RWMutex
var data dataset

type dataset struct {
	Source       string      `json:"source"`
	SnapshotDate string      `json:"snapshot_date"`
	Entries      []Candidate `json:"entries"`
}

// Lookup preserves the request identity and returns all exact base-ID candidates.
// It never silently chooses a provider or strips model version/tier suffixes.
func Lookup(request string) Result {
	initialize()
	dataMu.RLock()
	defer dataMu.RUnlock()
	id := strings.TrimSpace(request)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	out := Result{RequestModel: request, Source: data.Source, SnapshotDate: data.SnapshotDate, Status: "unknown", Candidates: []Candidate{}}
	for _, candidate := range data.Entries {
		if strings.EqualFold(candidate.ModelID, id) {
			out.Candidates = append(out.Candidates, candidate)
		}
	}
	sort.Slice(out.Candidates, func(i, j int) bool { return out.Candidates[i].Provider < out.Candidates[j].Provider })
	if len(out.Candidates) > 0 {
		out.Status = "reference_confirmation_required"
	}
	return out
}
