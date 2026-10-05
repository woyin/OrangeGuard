package monitor

import (
	"fmt"
	"testing"
	"time"
)

func TestRecordAndSnapshot(t *testing.T) {
	r := New(nil)
	r.Record("gpt-6-astra", "", "gpt-6-astra-2026-08-01", false)
	r.Record("gpt-6-astra", "", "gpt-5.5-mini", false)
	r.Record("gpt-6-astra", "", "", false) // no model reported
	r.Record("gpt-6-astra", "", "", true)  // failed
	r.Record("glm-5.2", "my-glm-5.2", "glm-5.2", false)
	r.Record("glm-5.2", "my-glm-5.2", "my-glm-5.2", false) // alias accepted too
	r.Record("", "", "x", false)                           // ignored

	snap := r.Snapshot()
	if len(snap) != 2 || snap[0].Model != "gpt-6-astra" {
		t.Fatalf("mismatching model must sort first: %+v", snap)
	}
	g := snap[0]
	if g.Requests != 4 || g.Failed != 1 || g.Reported != 2 || g.Mismatches != 1 || g.MismatchRate != 0.5 || g.LastMismatchAs != "gpt-5.5-mini" {
		t.Fatalf("gpt stats = %+v", g)
	}
	if snap[1].Mismatches != 0 || len(snap[1].Aliases) != 1 || snap[1].Aliases[0] != "my-glm-5.2" {
		t.Fatalf("glm stats = %+v", snap[1])
	}
}

func TestBounds(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	r := New(func() time.Time { clock = clock.Add(time.Second); return clock })
	for i := 0; i < maxModels+5; i++ {
		r.Record(fmt.Sprintf("m%d", i), "", "", false)
	}
	if n := len(r.Snapshot()); n != maxModels {
		t.Fatalf("models = %d, want %d", n, maxModels)
	}
	for i := 0; i < maxServedModels+3; i++ {
		r.Record("busy", "", fmt.Sprintf("served-%d", i), false)
	}
	for _, st := range r.Snapshot() {
		if st.Model == "busy" && len(st.Served) != maxServedModels {
			t.Fatalf("served names = %d", len(st.Served))
		}
	}
	r.Reset()
	if len(r.Snapshot()) != 0 {
		t.Fatal("reset failed")
	}
}
