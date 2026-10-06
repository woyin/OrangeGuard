package engine

import (
	"encoding/json"
	"github.com/woyin/orangeguard/internal/catalog"
	"testing"
)

func TestCatalogManagement(t *testing.T) {
	e := newEngine(t, newFakeHost(nil), "guard:\n  max_retries: 0\n")
	status, body := e.HandleManagement("GET", "/v0/management"+ModelsPath, map[string][]string{"model": {"z.ai/glm-5.3-flash"}}, nil)
	var r catalog.Result
	if status != 200 || json.Unmarshal(body, &r) != nil || r.RequestModel != "z.ai/glm-5.3-flash" || len(r.Candidates) == 0 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	status, _ = e.HandleManagement("GET", ModelsPath, nil, nil)
	if status != 400 {
		t.Fatalf("missing model status=%d", status)
	}
}
