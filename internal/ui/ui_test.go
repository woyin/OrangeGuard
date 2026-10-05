package ui

import (
	"strings"
	"testing"
)

func TestPageIsSelfContained(t *testing.T) {
	page := string(Page)
	for _, want := range []string{"<!doctype html>", "/plugins/orangeguard/status", "/plugins/orangeguard/config"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page is missing %q", want)
		}
	}
	// The page is served under a strict CSP; external scripts and styles would be blocked.
	for _, banned := range []string{"<script src", "<link rel=\"stylesheet\"", "https://"} {
		if strings.Contains(page, banned) {
			t.Fatalf("page must not load external resources (%q)", banned)
		}
	}
}
