package catalog

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRefreshPersistsAndFailureRetainsData(t *testing.T) {
	initialize()
	dataMu.RLock()
	original := data
	dataMu.RUnlock()
	defer func() { dataMu.Lock(); data = original; dataMu.Unlock() }()
	t.Setenv("ORANGEGUARD_CATALOG_CACHE", filepath.Join(t.TempDir(), "catalog.json"))
	old := http.DefaultClient
	defer func() { http.DefaultClient = old }()
	body := `{"test":{"models":{"test-model":{"name":"Test","limit":{"context":1000,"output":100},"modalities":{"input":["text"],"output":["text"]},"reasoning":true}}}}`
	http.DefaultClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != sourceURL {
			t.Fatalf("unexpected source %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	if err := Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cachePath())
	if err != nil || !strings.Contains(string(raw), "test-model") {
		t.Fatalf("cache not persisted: %v", err)
	}
	if len(Lookup("channel/test-model").Candidates) != 1 {
		t.Fatal("refreshed catalog not visible")
	}
	body = `{"error":"invalid payload"}`
	if err := Refresh(context.Background()); err == nil {
		t.Fatal("empty data accepted")
	}
	if len(Lookup("channel/test-model").Candidates) != 1 {
		t.Fatal("failed refresh destroyed valid catalog")
	}
	if Status()["last_error"] == "" {
		t.Fatal("failure not visible")
	}
}

func TestParseUpstreamRejectsMalformed(t *testing.T) {
	for _, raw := range []string{"not-json", "{}", "[]"} {
		if _, err := parseUpstream([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
