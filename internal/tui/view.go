package tui

import (
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/nfnt/resize"

	apptypes "DevStarByte/internal/types"
)

// ── View ──────────────────────────────────────────────────────────────────────

func (m Model) View() string {
	if m.width < 40 || m.height < 10 {
		return fmt.Sprintf("Terminal too small (%dx%d). Please resize.\n", m.width, m.height)
	}

	// Dimensions:
	//   header:    1 line  (no border)
	//   mainRow:   innerH + 2 lines (border)
	//   inputBar:  1 + 2  = 3 lines (border)
	//   statusBar: 1 line  (no border)
	//   total = 1 + (innerH+2) + 3 + 1 = innerH + 7
	innerH := m.height - 7
	if innerH < 3 {
		innerH = 3
	}

	// Panel inner widths.  outer = inner + 2 (border).
	//   chatOuter + msgOuter = m.width
	//   (chatInner+2) + (msgInner+2) = m.width
	//   chatInner + msgInner = m.width - 4
	chatInner := 28
	msgInner := m.width - chatInner - 4
	if msgInner < 10 {
		msgInner = 10
	}

	// ── Header ────────────────────────────────────────────────────────────────
	header := sHeader.Width(m.width - 2).
		Render("WhatsApp TUI    Tab: switch panels    q: quit")

	// ── Chat list panel ───────────────────────────────────────────────────────
	chatContent := m.renderChatList(chatInner, innerH)
	chatBorder := sIdle
	if m.focus == focusChatList {
		chatBorder = sActive
	}
	chatBox := chatBorder.Width(chatInner).MaxWidth(chatInner + 2).Height(innerH).Render(chatContent)

	// ── Message panel ─────────────────────────────────────────────────────────
	msgContent := m.renderMessages(msgInner, innerH)
	msgBorder := sIdle
	if m.focus == focusMessages {
		msgBorder = sActive
	}
	msgBox := msgBorder.Width(msgInner).MaxWidth(msgInner + 2).Height(innerH).Render(msgContent)

	mainRow := lipgloss.JoinHorizontal(lipgloss.Top, chatBox, msgBox)

	// ── Input bar ─────────────────────────────────────────────────────────────
	inputBar := m.renderInput(m.width)

	// ── Status bar ────────────────────────────────────────────────────────────
	statusBar := m.renderStatus()

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		mainRow,
		inputBar,
		statusBar,
	)
}

// ── Chat list rendering ───────────────────────────────────────────────────────

func (m Model) renderChatList(w, h int) string {
	title := sAccent.Bold(true).Render("Chats")
	divider := sTime.Render(strings.Repeat("─", w))
	lines := []string{title, divider}

	visRows := h - 2
	if visRows < 1 {
		visRows = 1
	}

	end := min(m.chatScroll+visRows, len(m.chats))
	for i := m.chatScroll; i < end; i++ {
		c := m.chats[i]
		name := truncateStr(c.Name, w-6)
		badge := ""
		if c.Unread > 0 {
			badge = " " + sUnread.Render(fmt.Sprintf("(%d)", c.Unread))
		}
		row := name + badge
		if i == m.selectedChat {
			lines = append(lines, sChatSel.Width(w).Render(row))
		} else {
			lines = append(lines, sChatNorm.Width(w).Render(row))
		}
	}

	if len(m.chats) == 0 {
		lines = append(lines, sMuted.Render("No chats yet – waiting for messages…"))
	}

	return clampContent(strings.Join(lines, "\n"), w)
}

// ── Message panel rendering ───────────────────────────────────────────────────

func (m Model) renderMessages(w, h int) string {
	var chatName, key string
	if m.selectedChat >= 0 && m.selectedChat < len(m.chats) {
		c := m.chats[m.selectedChat]
		chatName = c.Name
		if c.IsGroup {
			chatName += " (group)"
		}
		key = c.JID.String()
	}

	title := sAccent.Bold(true).Render(orDefault(chatName, "Select a chat"))
	divider := sTime.Render(strings.Repeat("─", w))
	header := []string{title, divider}

	if key == "" {
		hint := sMuted.Render("Use ↑/↓ to navigate the list, Enter or Tab to open a chat.")
		return strings.Join(append(header, hint), "\n")
	}

	// Always read from the global map so history-sync'd messages are immediately visible.
	m.state.MessagesMu.RLock()
	msgs := make([]apptypes.Message, len(m.state.MessagesMap[key]))
	copy(msgs, m.state.MessagesMap[key])
	m.state.MessagesMu.RUnlock()

	var msgLines []string
	for _, msg := range msgs {
		msgLines = append(msgLines, m.formatMsg(msg, w)...)
		msgLines = append(msgLines, "") // blank separator
	}

	visH := h - 2
	if visH < 1 {
		visH = 1
	}
	total := len(msgLines)
	offset := m.msgScroll
	if offset+visH > total {
		offset = max(0, total-visH)
	}

	var visible []string
	if total > 0 && offset < total {
		end := min(offset+visH, total)
		visible = msgLines[offset:end]
	} else if total == 0 {
		visible = []string{sMuted.Render("No messages yet. Type below and press Enter.")}
	}

	return clampContent(strings.Join(append(header, visible...), "\n"), w)
}

func (m Model) formatMsg(msg apptypes.Message, w int) []string {
	ts := sTime.Render(msg.Timestamp.Format("15:04"))
	var lines []string

	if msg.FromMe {
		meta := sTime.Render("You · ") + ts
		lines = append(lines, clampWidth("  "+meta, w))
		for _, l := range wordWrap(msg.Content, w-6) {
			lines = append(lines, clampWidth("  "+sMyMsg.Render(l), w))
		}
	} else {
		meta := sSender.Render(msg.Sender) + "  " + ts
		lines = append(lines, clampWidth(meta, w))
		for _, l := range wordWrap(msg.Content, w-4) {
			lines = append(lines, clampWidth(sTheirMsg.Render(l), w))
		}
	}

	// Render image inline if available.
	if msg.ImagePath != "" {
		if imgLines := renderImageBlock(msg.ImagePath, w-4); len(imgLines) > 0 {
			lines = append(lines, imgLines...)
		}
	}

	return lines
}

// imageRenderCache caches rendered terminal output per image path+width so
// repeated View() calls don't re-render or re-exec chafa.
var imageRenderCache sync.Map

// renderImageBlock renders an image for the terminal.  It tries chafa(1)
// first (which uses braille / block characters / sixel depending on the
// terminal and produces much sharper output), then falls back to the
// built-in half-block renderer.
func renderImageBlock(imgPath string, maxCols int) []string {
	cacheKey := fmt.Sprintf("%s:%d", imgPath, maxCols)
	if cached, ok := imageRenderCache.Load(cacheKey); ok {
		return cached.([]string)
	}

	lines := renderImageWithChafa(imgPath, maxCols)
	if lines == nil {
		lines = renderImageHalfBlock(imgPath, maxCols)
	}

	if lines != nil {
		imageRenderCache.Store(cacheKey, lines)
	}
	return lines
}

// renderImageWithChafa shells out to chafa(1) for high-quality terminal
// image rendering.  Returns nil if chafa is not installed.
func renderImageWithChafa(imgPath string, maxCols int) []string {
	cols := maxCols
	rows := cols * 3 / 8 // roughly 3:8 aspect for compact look
	cmd := exec.Command("chafa",
		"--format", "symbols",
		"--symbols", "all",
		"--size", fmt.Sprintf("%dx%d", cols, rows),
		imgPath,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	result := strings.Split(strings.TrimRight(string(out), "\n\r"), "\n")
	if len(result) == 0 || (len(result) == 1 && result[0] == "") {
		return nil
	}
	return result
}

// renderImageHalfBlock converts an image into ANSI true-color half-block
// characters (▄) as a fallback when chafa is not available.
func renderImageHalfBlock(imgPath string, maxCols int) []string {
	f, err := os.Open(imgPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil
	}

	// Resize to fit the panel width (compact: max 40 cols).
	targetW := maxCols
	if targetW > 40 {
		targetW = 40
	}
	if targetW < 10 {
		targetW = 10
	}
	img = resize.Resize(uint(targetW), 0, img, resize.Lanczos3)

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	var lines []string
	// Process 2 rows of pixels at a time → 1 terminal row.
	for y := bounds.Min.Y; y < bounds.Min.Y+h; y += 2 {
		var sb strings.Builder
		for x := bounds.Min.X; x < bounds.Min.X+w; x++ {
			// Top pixel → background colour.
			rt, gt, bt, _ := img.At(x, y).RGBA()
			tr, tg, tb := rt>>8, gt>>8, bt>>8

			// Bottom pixel → foreground colour (may be out of bounds).
			var br, bg, bb uint32
			if y+1 < bounds.Min.Y+h {
				rb, gb, bb2, _ := img.At(x, y+1).RGBA()
				br, bg, bb = rb>>8, gb>>8, bb2>>8
			} else {
				br, bg, bb = tr, tg, tb
			}

			// \033[48;2;R;G;Bm = set background, \033[38;2;R;G;Bm = set foreground
			fmt.Fprintf(&sb, "\033[48;2;%d;%d;%dm\033[38;2;%d;%d;%dm▄",
				tr, tg, tb, br, bg, bb)
		}
		sb.WriteString("\033[0m") // reset
		lines = append(lines, sb.String())
	}
	return lines
}

// ── Input bar rendering ───────────────────────────────────────────────────────

func (m Model) renderInput(totalW int) string {
	active := m.focus == focusInput
	r := []rune(m.inputText)
	hint := sTime.Render("[Enter] send  [Esc] back  [Ctrl+W] del-word  [Tab] switch")
	prefix := sAccent.Render("> ")

	// Available space for the typed text (inside the border, minus prefix and hint).
	innerW := totalW - lipgloss.Width(hint) - lipgloss.Width(prefix) - 4
	if innerW < 1 {
		innerW = 1
	}

	var display string
	if active {
		// Scroll the visible window so the cursor is always visible.
		startR := max(0, m.inputCursor-innerW/2)
		endR := min(len(r), startR+innerW)
		sub := r[startR:endR]
		curIdx := m.inputCursor - startR

		if curIdx < len(sub) {
			before := string(sub[:curIdx])
			cur := lipgloss.NewStyle().Reverse(true).Render(string(sub[curIdx : curIdx+1]))
			after := string(sub[curIdx+1:])
			display = before + cur + after
		} else {
			display = string(sub) + lipgloss.NewStyle().Reverse(true).Render(" ")
		}
	} else if m.inputText == "" {
		display = sMuted.Render("Tab to focus · select a chat first")
	} else {
		display = m.inputText
	}

	content := prefix + lipgloss.NewStyle().Width(innerW).Render(display) + hint

	border := sIdle
	if active {
		border = sActive
	}
	return border.Width(totalW - 4).Height(1).Render(content)
}

// ── Status bar rendering ──────────────────────────────────────────────────────

func (m Model) renderStatus() string {
	conn := sAccent.Render("● Connected")

	// Sync indicator.
	var syncStatus string
	if m.syncDone {
		syncStatus = "   " + lipgloss.NewStyle().Foreground(clrGreen).Render("Synced ✓")
	} else if m.syncCount > 0 {
		syncStatus = "   " + lipgloss.NewStyle().Foreground(clrMuted).Render(fmt.Sprintf("Syncing… (%d)", m.syncCount))
	} else {
		syncStatus = "   " + lipgloss.NewStyle().Foreground(clrMuted).Render("Syncing…")
	}

	flash := ""
	if m.statusMsg != "" && time.Since(m.statusTime) < 4*time.Second {
		flash = "   " + lipgloss.NewStyle().Foreground(clrText).Render(m.statusMsg)
	}
	keys := sTime.Render("  j/k navigate · g/G top/bottom · i type · q quit")
	return sStatus.Width(m.width).Render(conn + syncStatus + flash + keys)
}

// ── Dimension helpers ─────────────────────────────────────────────────────────

// visibleChatRows returns how many chat items fit in the list panel.
func (m Model) visibleChatRows() int {
	innerH := m.height - 7
	return max(1, innerH-2) // subtract the title + divider rows
}

// maxMsgScroll returns the maximum scroll offset for the given chat.
func (m Model) maxMsgScroll(key string) int {
	m.state.MessagesMu.RLock()
	msgs := make([]apptypes.Message, len(m.state.MessagesMap[key]))
	copy(msgs, m.state.MessagesMap[key])
	m.state.MessagesMu.RUnlock()
	approxW := m.width - 28 - 4 - 4 // rough inner message width
	var lines []string
	for _, msg := range msgs {
		lines = append(lines, m.formatMsg(msg, approxW)...)
		lines = append(lines, "")
	}
	innerH := m.height - 7
	visH := max(1, innerH-2)
	return max(0, len(lines)-visH)
}

// ── Text utilities ────────────────────────────────────────────────────────────

// mergeMessages merges two message slices, deduplicates by ID, and sorts by time.
func mergeMessages(a, b []apptypes.Message) []apptypes.Message {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]apptypes.Message, 0, len(a)+len(b))
	for _, m := range append(append([]apptypes.Message{}, a...), b...) {
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	return out
}

// truncateStr truncates s to maxW runes, appending "…" if needed.
func truncateStr(s string, maxW int) string {
	r := []rune(s)
	if len(r) <= maxW {
		return s
	}
	if maxW <= 1 {
		return "…"
	}
	return string(r[:maxW-1]) + "…"
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
