// Package ui embeds the orangeguard management page.
package ui

import _ "embed"

// Page is the single-file management page (HTML, CSS and JS inline).
//
//go:embed index.html
var Page []byte
