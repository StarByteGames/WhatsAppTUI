package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"go.mau.fi/whatsmeow/types"

	"DevStarByte/internal/state"
	apptypes "DevStarByte/internal/types"
)

// ── Layout ────────────────────────────────────────────────────────────────────

// layout holds the panel dimensions derived from the terminal size. It is the
// single source of truth for rendering and scrolling.
//
//	header:    1 line  (no border)
//	mainRow:   innerH + 2 lines (border)
//	inputBar:  1 + 2 = 3 lines (border)
//	statusBar: 1 line  (no border)
//	total = innerH + 7
type layout struct {
	innerH    int // inner height of the chat and message panels
	chatInner int // inner width of the chat list
	msgInner  int // inner width of the message panel
	chatRows  int // visible chat list rows (minus title + divider)
	msgRows   int // visible message lines (minus title + divider)
}

func (m Model) layout() layout {
	l := layout{innerH: max(3, m.height-7), chatInner: 28}
	// (chatInner+2) + (msgInner+2) = width
	l.msgInner = max(10, m.width-l.chatInner-4)
	l.chatRows = max(1, l.innerH-2)
	l.msgRows = max(1, l.innerH-2)
	return l
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m Model) View() string {
	if m.width < 40 || m.height < 10 {
		return fmt.Sprintf("Terminal too small (%dx%d). Please resize.\n", m.width, m.height)
	}
	l := m.layout()

	header := sHeader.Width(m.width - 2).
		Render("WhatsApp TUI    Tab: switch panels    /: search chats    q: quit")

	chatBorder := sIdle
	if m.focus == focusChatList || m.focus == focusSearch {
		chatBorder = sActive
	}
	chatBox := chatBorder.Width(l.chatInner).MaxWidth(l.chatInner + 2).Height(l.innerH).
		Render(m.renderChatList(l))

	msgBorder := sIdle
	if m.focus == focusMessages {
		msgBorder = sActive
	}
	msgBox := msgBorder.Width(l.msgInner).MaxWidth(l.msgInner + 2).Height(l.innerH).
		Render(m.renderMessages(l))

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		lipgloss.JoinHorizontal(lipgloss.Top, chatBox, msgBox),
		m.renderInput(m.width),
		m.renderStatus(),
	)
}

// ── Chat list rendering ───────────────────────────────────────────────────────

func (m Model) renderChatList(l layout) string {
	w := l.chatInner
	lines := []string{
		m.renderChatTitle(w),
		sDivider.Render(strings.Repeat("─", w)),
	}

	end := min(m.chatScroll+l.chatRows, len(m.chats))
	for i := m.chatScroll; i < end; i++ {
		c := m.chats[i]
		badge := ""
		if c.Unread > 0 {
			badge = " " + sUnread.Render(fmt.Sprintf("(%d)", c.Unread))
		}
		pin := ""
		if c.Pinned() {
			pin = "📌 "
		}
		row := pin + truncateStr(displayName(c), w-2-lipgloss.Width(pin)-lipgloss.Width(badge)) + badge
		if i == m.selectedChat {
			lines = append(lines, sChatSel.Width(w).Render(row))
		} else {
			lines = append(lines, sChatNorm.Width(w).Render(row))
		}
	}

	switch {
	case len(m.chats) > 0:
	case m.search != "":
		lines = append(lines, sMuted.Render("No chat matches."))
	default:
		lines = append(lines, sMuted.Render("No chats yet – waiting for messages…"))
	}

	return clampContent(strings.Join(lines, "\n"), w)
}

// renderChatTitle shows the search bar while searching or filtering, and the
// panel title otherwise.
func (m Model) renderChatTitle(w int) string {
	if m.focus != focusSearch && m.search == "" {
		return sAccent.Bold(true).Render("Chats") + sTime.Render("  / to search")
	}
	count := sTime.Render(fmt.Sprintf(" %d", len(m.chats)))
	query := truncateStr(m.search, max(1, w-4-lipgloss.Width(count)))
	if m.focus == focusSearch {
		query += lipgloss.NewStyle().Reverse(true).Render(" ")
	}
	return sAccent.Bold(true).Render("/ ") + query + count
}

// displayName is the name shown for a chat. Chats without a known name show
// their phone number, or a label if not even that is known (LIDs are
// anonymous IDs, not phone numbers).
func displayName(c apptypes.ChatItem) string {
	switch {
	case state.HasRealName(c):
		return c.Name
	case c.IsGroup:
		return "Unnamed group"
	case c.JID.Server == types.HiddenUserServer:
		return "Unknown contact"
	case c.JID.User != "":
		return "+" + c.JID.User
	}
	return c.Name
}

// ── Message panel rendering ───────────────────────────────────────────────────

func (m Model) renderMessages(l layout) string {
	w := l.msgInner
	key := m.selectedKey()
	title := "Select a chat"
	if key != "" {
		c := m.chats[m.selectedChat]
		title = displayName(c)
		if c.IsGroup {
			title += " (group)"
		}
	}
	header := []string{
		sAccent.Bold(true).Render(truncateStr(title, w)),
		sDivider.Render(strings.Repeat("─", w)),
	}

	if key == "" {
		hint := sMuted.Render("Use ↑/↓ to navigate the list, Enter or Tab to open a chat.")
		return strings.Join(append(header, hint), "\n")
	}

	msgLines := m.messageLines(key, w)
	total := len(msgLines)
	if total == 0 {
		hint := sMuted.Render("No messages yet. Type below and press Enter.")
		return strings.Join(append(header, hint), "\n")
	}

	maxOffset := max(0, total-l.msgRows)
	offset := maxOffset // msgScroll < 0: stick to the bottom
	if m.msgScroll >= 0 {
		offset = min(m.msgScroll, maxOffset)
	}
	visible := msgLines[offset:min(offset+l.msgRows, total)]

	// Show that there is more below when scrolled up.
	if offset < maxOffset && len(visible) > 0 {
		visible[len(visible)-1] = sMuted.Render(fmt.Sprintf("  ↓ %d more lines (G to jump to bottom)", maxOffset-offset))
	}

	return clampContent(strings.Join(append(header, visible...), "\n"), w)
}

// messageLines renders all messages of a chat into terminal lines.
func (m Model) messageLines(key string, w int) []string {
	var lines []string
	var lastDate string
	for _, msg := range m.state.Messages(key) {
		// Date separator whenever the day changes.
		if dateStr := msg.Timestamp.Format("Jan 2, 2006"); dateStr != lastDate {
			lastDate = dateStr
			label := sDateBadge.Render("── " + dateStr + " ──")
			pad := max(0, (w-lipgloss.Width(label))/2)
			lines = append(lines, strings.Repeat(" ", pad)+label, "")
		}
		lines = append(lines, m.formatMsg(msg, w)...)
		lines = append(lines, "")
	}
	return lines
}

// maxMsgScroll returns the largest scroll offset for the selected chat.
func (m Model) maxMsgScroll() int {
	key := m.selectedKey()
	if key == "" {
		return 0
	}
	l := m.layout()
	return max(0, len(m.messageLines(key, l.msgInner))-l.msgRows)
}

func (m Model) formatMsg(msg apptypes.Message, w int) []string {
	textW := max(1, w-6)

	// Meta line: sender, time and state tags.
	var meta string
	if msg.FromMe {
		meta = sTime.Render("You")
	} else {
		meta = sSender.Render(msg.Sender)
	}
	meta += "  " + sTime.Render(msg.Timestamp.Format("15:04"))
	if len(msg.Edits) > 0 {
		meta += sTime.Render(" · edited")
		if !m.showEdits {
			meta += sTime.Render(fmt.Sprintf(" (%d, e to show)", len(msg.Edits)))
		}
	}
	if msg.Deleted {
		meta += sDeletedTag.Render(" · deleted")
	}

	var body []string

	// Previous versions of an edited message, oldest first.
	if m.showEdits {
		for _, v := range msg.Edits {
			body = append(body, sEditLabel.Render("✎ "+formatVersionTime(v.Timestamp, msg.Timestamp)))
			for _, l := range wordWrap(v.Content, textW) {
				body = append(body, sEditOld.Render(l))
			}
		}
		if len(msg.Edits) > 0 {
			body = append(body, sEditLabel.Render("✎ current"))
		}
	}

	bubble := sTheirMsg
	if msg.FromMe {
		bubble = sMyMsg
	}
	content := msg.Content
	if msg.Deleted {
		bubble = bubble.Foreground(clrDeleted)
		if msg.IsPlaceholder() {
			bubble = bubble.Italic(true)
			content = "⊘ This message was deleted"
		}
	}
	if content != "" {
		for _, l := range wordWrap(content, textW) {
			body = append(body, bubble.Render(l))
		}
	}

	lines := []string{meta}
	lines = append(lines, body...)
	if msg.FromMe {
		// Right-align own messages.
		for i, line := range lines {
			lines[i] = strings.Repeat(" ", max(0, w-lipgloss.Width(line)-1)) + line
		}
	} else {
		for i, line := range lines {
			lines[i] = clampWidth(line, w)
		}
	}

	if msg.ImagePath != "" {
		lines = append(lines, renderImageBlock(msg.ImagePath, w-4)...)
	}
	return lines
}

// formatVersionTime shows only the time if the version is from the same day
// as the original message, otherwise also the date.
func formatVersionTime(t, original time.Time) string {
	if t.Format("2006-01-02") == original.Format("2006-01-02") {
		return t.Format("15:04")
	}
	return t.Format("Jan 2, 15:04")
}

// ── Input bar rendering ───────────────────────────────────────────────────────

func (m Model) renderInput(totalW int) string {
	active := m.focus == focusInput
	r := []rune(m.inputText)
	hint := sTime.Render("[Enter] send  [Esc] back  [Ctrl+W] del-word  [Tab] switch")
	prefix := sAccent.Render("> ")

	// Space for the typed text (inside the border, minus prefix and hint).
	innerW := max(1, totalW-lipgloss.Width(hint)-lipgloss.Width(prefix)-4)

	var display string
	switch {
	case active:
		// Scroll the visible window so the cursor is always visible.
		startR := max(0, m.inputCursor-innerW/2)
		endR := min(len(r), startR+innerW-1)
		sub := r[startR:endR]
		curIdx := m.inputCursor - startR
		cursor := lipgloss.NewStyle().Reverse(true)
		if curIdx < len(sub) {
			display = string(sub[:curIdx]) + cursor.Render(string(sub[curIdx])) + string(sub[curIdx+1:])
		} else {
			display = string(sub) + cursor.Render(" ")
		}
	case m.inputText == "":
		display = sMuted.Render("Tab to focus · select a chat first")
	default:
		display = truncateStr(m.inputText, innerW)
	}

	content := prefix + lipgloss.NewStyle().Width(innerW).MaxHeight(1).Render(display) + hint

	border := sIdle
	if active {
		border = sActive
	}
	return border.Width(totalW - 4).Height(1).Render(content)
}

// ── Status bar rendering ──────────────────────────────────────────────────────

func (m Model) renderStatus() string {
	conn := sAccent.Render("● Connected")
	if !m.state.Connected() {
		conn = sDeletedTag.Render("● Disconnected")
	}

	var syncStatus string
	switch {
	case m.syncDone:
		syncStatus = "   " + sAccent.Render("Synced ✓")
	case m.syncCount > 0:
		syncStatus = "   " + sTime.Render(fmt.Sprintf("Syncing… (%d)", m.syncCount))
	default:
		syncStatus = "   " + sTime.Render("Syncing…")
	}

	flash := ""
	if m.statusMsg != "" && time.Since(m.statusTime) < 4*time.Second {
		flash = "   " + lipgloss.NewStyle().Foreground(clrText).Render(m.statusMsg)
	}
	keys := sTime.Render("  j/k navigate · / search · g/G top/bottom · e edits · i type · q quit")
	if m.focus == focusSearch {
		keys = sTime.Render("  type to filter · ↑/↓ select · Enter open · Tab keep filter · Esc clear")
	}
	return sStatus.Width(m.width).MaxHeight(1).Render(conn + syncStatus + flash + keys)
}
