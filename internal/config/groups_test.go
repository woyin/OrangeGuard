package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestGroupDepthAndSharedDAG(t *testing.T) {
	for _, n := range []int{8, 9} {
		var b strings.Builder
		b.WriteString("virtual_models:\n")
		for i := 0; i < n; i++ {
			next := "gpt-a"
			if i+1 < n {
				next = fmt.Sprintf("g%d", i+1)
			}
			fmt.Fprintf(&b, "  - name: g%d\n    members: [{model: %s}]\n", i, next)
		}
		_, _, err := Parse([]byte(b.String()))
		if (err == nil) != (n == 8) {
			t.Fatalf("depth %d: %v", n, err)
		}
	}
	raw := "virtual_models:\n  - name: root\n    members: [{model: left}, {model: right}]\n  - name: left\n    members: [{model: shared}]\n  - name: right\n    members: [{model: shared}]\n  - name: shared\n    members: [{model: gpt-a}]\n"
	if _, _, err := Parse([]byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestRejectGroupCycles(t *testing.T) {
	for _, raw := range []string{
		"virtual_models:\n  - name: a\n    members: [{model: a}]\n",
		"virtual_models:\n  - name: a\n    members: [{model: b}]\n  - name: b\n    members: [{model: a}]\n",
		"virtual_models:\n  - name: a\n    members: [{model: b, expect: gpt-x}]\n  - name: b\n    members: [{model: gpt-x}]\n",
	} {
		if _, _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("invalid group graph accepted: %s", raw)
		}
	}
}
