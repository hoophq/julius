// Package ui renders julius CLI output: ANSI color, meters, and bars.
// Styling is cosmetic only — every function degrades to plain text when
// stdout is not a terminal or NO_COLOR is set, so piped output and tests
// see stable, uncolored strings.
package ui

import (
	"fmt"
	"os"
	"strings"
)

var enabled = detectColor()

func detectColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("CLICOLOR_FORCE") == "1" {
		return true
	}
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
)

func paint(code, s string) string {
	if !enabled || s == "" {
		return s
	}
	return code + s + reset
}

// Title styles a section heading.
func Title(s string) string { return paint(bold+cyan, s) }

// Good styles a healthy value.
func Good(s string) string { return paint(green, s) }

// Warn styles a value needing attention.
func Warn(s string) string { return paint(yellow, s) }

// Bad styles a failure or regression.
func Bad(s string) string { return paint(red, s) }

// Dim styles secondary text (labels, units, notes).
func Dim(s string) string { return paint(dim, s) }

// Bold styles emphasized text.
func Bold(s string) string { return paint(bold, s) }

// Pct colors a savings percentage by how healthy it is.
func Pct(pct float64) string {
	s := fmt.Sprintf("%3.0f%%", pct)
	switch {
	case pct >= 60:
		return Good(s)
	case pct >= 25:
		return Warn(s)
	case pct < 0:
		return Bad(s)
	default:
		return s
	}
}

// Meter renders a horizontal gauge for a 0–100 percentage.
func Meter(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct/100*float64(width) + 0.5)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	switch {
	case pct >= 60:
		return Good(bar)
	case pct >= 25:
		return Warn(bar)
	default:
		return Bad(bar)
	}
}

// Bar renders a proportional impact bar (value relative to max).
func Bar(value, max, width int) string {
	if max <= 0 || value <= 0 {
		return ""
	}
	n := value * width / max
	if n < 1 {
		n = 1
	}
	return paint(cyan, strings.Repeat("▪", n))
}
