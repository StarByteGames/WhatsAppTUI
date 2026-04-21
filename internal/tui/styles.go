package tui

import "github.com/charmbracelet/lipgloss"

// ── Colour palette (WhatsApp dark theme) ─────────────────────────────────────

var (
	clrGreen    = lipgloss.Color("#25D366")
	clrPanel    = lipgloss.Color("#111B21")
	clrBorder   = lipgloss.Color("#2A3942")
	clrText     = lipgloss.Color("#E9EDEF")
	clrMuted    = lipgloss.Color("#8696A0")
	clrMyBg     = lipgloss.Color("#005C4B")
	clrTheirBg  = lipgloss.Color("#202C33")
	clrHeaderBg = lipgloss.Color("#202C33")
	clrUnread   = lipgloss.Color("#00A884")
)

// ── Lipgloss styles ───────────────────────────────────────────────────────────

var (
	sActive = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(clrGreen)

	sIdle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(clrBorder)

	sHeader = lipgloss.NewStyle().
		Bold(true).
		Background(clrHeaderBg).
		Foreground(clrGreen).
		PaddingLeft(1).
		PaddingRight(1)

	sChatSel = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#000000")).
			Background(clrGreen).
			PaddingLeft(1).
			PaddingRight(1)

	sChatNorm = lipgloss.NewStyle().
			Foreground(clrText).
			PaddingLeft(1).
			PaddingRight(1)

	sUnread = lipgloss.NewStyle().
		Foreground(clrUnread).
		Bold(true)

	sSender = lipgloss.NewStyle().
		Bold(true).
		Foreground(clrGreen)

	sTime = lipgloss.NewStyle().
		Foreground(clrMuted)

	sMyMsg = lipgloss.NewStyle().
		Background(clrMyBg).
		Foreground(clrText).
		PaddingLeft(1).
		PaddingRight(1)

	sTheirMsg = lipgloss.NewStyle().
			Background(clrTheirBg).
			Foreground(clrText).
			PaddingLeft(1).
			PaddingRight(1)

	sStatus = lipgloss.NewStyle().
		Background(clrHeaderBg).
		Foreground(clrMuted)

	sMuted = lipgloss.NewStyle().
		Foreground(clrMuted).
		Italic(true)

	sAccent = lipgloss.NewStyle().
		Foreground(clrGreen)

	sDivider = lipgloss.NewStyle().
			Foreground(clrBorder)

	sDateBadge = lipgloss.NewStyle().
			Foreground(clrMuted).
			Bold(true)
)
