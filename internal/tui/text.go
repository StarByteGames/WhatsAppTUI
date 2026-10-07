package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── Text utilities ────────────────────────────────────────────────────────────

// truncateStr truncates s to maxW display columns, appending "…" if needed.
func truncateStr(s string, maxW int) string {
	if lipgloss.Width(s) <= maxW {
		return s
	}
	if maxW <= 1 {
		return "…"
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > maxW {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// wordWrap splits text into lines of at most width display columns, breaking at spaces.
// It handles embedded newlines by splitting on them first.
func wordWrap(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	// Handle embedded newlines.
	var result []string
	for _, paragraph := range strings.Split(text, "\n") {
		result = append(result, wrapLine(paragraph, width)...)
	}
	return result
}

// wrapLine wraps a single line (no embedded newlines) to the given display width.
func wrapLine(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var result []string
	r := []rune(text)
	for len(r) > 0 {
		if lipgloss.Width(string(r)) <= width {
			result = append(result, string(r))
			break
		}
		// Find the cut point where display width fits.
		cut := 0
		for cut < len(r) && lipgloss.Width(string(r[:cut+1])) <= width {
			cut++
		}
		if cut == 0 {
			cut = 1 // always consume at least one rune
		}
		// Try to break at a space.
		spaceCut := cut
		for spaceCut > 0 && r[spaceCut-1] != ' ' {
			spaceCut--
		}
		if spaceCut > 0 {
			cut = spaceCut
		}
		result = append(result, string(r[:cut]))
		r = r[cut:]
		for len(r) > 0 && r[0] == ' ' {
			r = r[1:]
		}
	}
	if len(result) == 0 {
		result = []string{""}
	}
	return result
}

// clampWidth truncates a (possibly styled/ANSI) string to at most maxW display columns.
func clampWidth(s string, maxW int) string {
	if lipgloss.Width(s) <= maxW {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(maxW).Render(s)
}

// clampContent truncates every line in content to maxW display columns.
func clampContent(content string, maxW int) string {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		lines[i] = clampWidth(l, maxW)
	}
	return strings.Join(lines, "\n")
}

// orDefault returns s if non-empty, otherwise def.
func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
