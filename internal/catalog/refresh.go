package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const sourceURL = "https://models.dev/api.json"
const maxBytes = 32 << 20

var refreshMu sync.Mutex
var refreshState struct {
	sync.Mutex
	LastAttempt time.Time
	LastSuccess time.Time
	LastError   string
	CacheError  string
}

func cachePath() string {
	if p := os.Getenv("ORANGEGUARD_CATALOG_CACHE"); p != "" {
		return p
	}
	return filepath.Join("plugins", "orangeguard-models-cache.json")
}

func initialize() {
	once.Do(func() {
		raw, err := files.ReadFile("snapshot.json")
		if err != nil {
			panic(err)
		}
		if err = json.Unmarshal(raw, &data); err != nil {
			panic(err)
		}
		if raw, err = os.ReadFile(cachePath()); err == nil {
			var cached dataset
			if json.Unmarshal(raw, &cached) == nil && len(cached.Entries) > 0 && cached.Source == sourceURL {
				data = cached
			}
		}
	})
}

var startOnce sync.Once

// Start enables the background scheduler once when the plugin is configured.
func Start() {
	initialize()
	startOnce.Do(func() {
		go func() {
			// Initial refresh is asynchronous: startup/model execution never waits on the network.
			_ = Refresh(context.Background())
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				_ = Refresh(context.Background())
			}
		}()
	})
}

func Status() map[string]any {
	initialize()
	dataMu.RLock()
	date, count := data.SnapshotDate, len(data.Entries)
	dataMu.RUnlock()
	refreshState.Lock()
	defer refreshState.Unlock()
	return map[string]any{"source": sourceURL, "snapshot_date": date, "entries": count, "interval_hours": 24, "last_attempt": refreshState.LastAttempt, "last_success": refreshState.LastSuccess, "last_error": refreshState.LastError, "cache_error": refreshState.CacheError, "cache_path": cachePath()}
}

// Refresh atomically replaces valid reference data; errors never destroy the last good catalog.
func Refresh(ctx context.Context) error {
	initialize()
	if !refreshMu.TryLock() {
		return fmt.Errorf("catalog refresh already in progress")
	}
	defer refreshMu.Unlock()
	refreshState.Lock()
	refreshState.LastAttempt = time.Now().UTC()
	refreshState.Unlock()
	err := refresh(ctx)
	refreshState.Lock()
	defer refreshState.Unlock()
	refreshState.LastError = ""
	if err != nil {
		refreshState.LastError = err.Error()
	} else {
		refreshState.LastSuccess = time.Now().UTC()
	}
	return err
}

func refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("models.dev HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxBytes {
		return fmt.Errorf("models.dev response exceeds 32 MiB")
	}
	next, err := parseUpstream(raw)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	// Disk failure must be visible, but should not discard usable in-memory data.
	cacheErr := persist(cachePath(), encoded)
	dataMu.Lock()
	data = next
	dataMu.Unlock()
	refreshState.Lock()
	refreshState.CacheError = ""
	if cacheErr != nil {
		refreshState.CacheError = cacheErr.Error()
	}
	refreshState.Unlock()
	return nil
}

func persist(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".orangeguard-catalog-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func parseUpstream(raw []byte) (dataset, error) {
	var providers map[string]struct {
		Models map[string]struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Updated     string `json:"last_updated"`
			Reasoning   bool   `json:"reasoning"`
			ToolCall    bool   `json:"tool_call"`
			Temperature bool   `json:"temperature"`
			Structured  bool   `json:"structured_output"`
			Limit       struct {
				Context int64 `json:"context"`
				Output  int64 `json:"output"`
			} `json:"limit"`
			Modalities struct {
				Input  []string `json:"input"`
				Output []string `json:"output"`
			} `json:"modalities"`
			Options []struct {
				Type   string   `json:"type"`
				Values []string `json:"values"`
			} `json:"reasoning_options"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &providers); err != nil {
		return dataset{}, fmt.Errorf("invalid models.dev JSON: %w", err)
	}
	out := dataset{Source: sourceURL, SnapshotDate: time.Now().UTC().Format(time.RFC3339)}
	for provider, p := range providers {
		for id, m := range p.Models {
			caps := map[string]any{}
			if m.Limit.Context > 0 {
				caps["context_length"] = m.Limit.Context
			}
			if m.Limit.Output > 0 {
				caps["max_output_tokens"] = m.Limit.Output
			}
			if len(m.Modalities.Input) > 0 {
				caps["input_modalities"] = m.Modalities.Input
			}
			if len(m.Modalities.Output) > 0 {
				caps["output_modalities"] = m.Modalities.Output
			}
			params := []string{}
			if m.ToolCall {
				params = append(params, "tools", "tool_choice")
			}
			if m.Temperature {
				params = append(params, "temperature")
			}
			if m.Structured {
				params = append(params, "response_format")
			}
			for _, opt := range m.Options {
				if opt.Type == "effort" && len(opt.Values) > 0 {
					caps["thinking"] = map[string]any{"levels": opt.Values}
					params = append(params, "reasoning_effort")
					break
				}
			}
			if len(params) > 0 {
				caps["supported_parameters"] = params
			}
			if len(caps) > 0 {
				out.Entries = append(out.Entries, Candidate{Provider: provider, ModelID: id, Name: m.Name, Capabilities: caps, Reasoning: m.Reasoning, Updated: m.Updated})
			}
		}
	}
	if len(out.Entries) == 0 {
		return dataset{}, fmt.Errorf("models.dev catalog contains no usable models")
	}
	return out, nil
}
