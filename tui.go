package main

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
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nfnt/resize"
	"go.mau.fi/whatsmeow/types"
)

// ── Focus areas ───────────────────────────────────────────────────────────────

type focusArea int

const (
	focusChatList focusArea = iota
	focusMessages
	focusInput
)

// ── Colour palette (WhatsApp dark theme) ─────────────────────────────────────

var (
	clrGreen   = lipgloss.Color("#25D366")
	clrPanel   = lipgloss.Color("#1F2C34")
	clrBorder  = lipgloss.Color("#374045")
	clrText    = lipgloss.Color("#E9EDEF")
	clrMuted   = lipgloss.Color("#8696A0")
	clrMyBg    = lipgloss.Color("#005C4B")
	clrTheirBg = lipgloss.Color("#1F2C34")
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
		Background(clrPanel).
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
		Foreground(clrGreen).
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
		Background(clrPanel).
		Foreground(clrMuted)

	sMuted = lipgloss.NewStyle().
		Foreground(clrMuted).
		Italic(true)

	sAccent = lipgloss.NewStyle().
		Foreground(clrGreen)
)

// ── Model ─────────────────────────────────────────────────────────────────────

// Model is the bubbletea application model.
type Model struct {
	width, height int
	focus         focusArea

	// Chat list state.
	chats        []ChatItem
	chatScroll   int
	selectedChat int

	// Message state.
	messages  map[string][]Message
	msgScroll int

	// Text input state.
	inputText   string
	inputCursor int // rune index

	// Temporary status flash.
	statusMsg  string
	statusTime time.Time
}

// NewModel creates an initialised Model.
func NewModel(chats []ChatItem) Model {
	msgs := make(map[string][]Message)
	messagesMu.RLock()
	for k, v := range messagesMap {
		msgs[k] = append([]Message{}, v...)
	}
	messagesMu.RUnlock()

	return Model{
		chats:    chats,
		messages: msgs,
	}
}

// ── Tea message types ─────────────────────────────────────────────────────────

type tuiNewMsg msgEvent
type tuiHistoryRefresh struct{}
type tuiLoadedMsgs struct {
	chatJID string
	msgs    []Message
}
type tuiStatus string
type tuiError struct{ err error }

// ── Init ──────────────────────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd {
	return tea.Batch(listenForMsg(), listenForHistory())
}

// loadChatMsgs fetches persisted messages for a chat from SQLite.
func loadChatMsgs(chatJID string) tea.Cmd {
	return func() tea.Msg {
		return tuiLoadedMsgs{chatJID: chatJID, msgs: loadMessagesFromDB(chatJID, 500)}
	}
}

// listenForHistory blocks until a history sync signal arrives, then delivers
// a tuiHistoryRefresh so the model can rebuild chats and messages from global state.
func listenForHistory() tea.Cmd {
	return func() tea.Msg {
		<-historyCh
		return tuiHistoryRefresh{}
	}
}

// listenForMsg blocks until a message arrives on incomingCh, then delivers it
// as a tuiNewMsg so the Update loop can process it.
func listenForMsg() tea.Cmd {
	return func() tea.Msg {
		return tuiNewMsg(<-incomingCh)
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tuiHistoryRefresh:
		return m.rebuildFromGlobal(), listenForHistory()

	case tuiLoadedMsgs:
		m.messages[msg.chatJID] = mergeMessages(m.messages[msg.chatJID], msg.msgs)
		if m.selectedChat >= 0 && m.selectedChat < len(m.chats) &&
			m.chats[m.selectedChat].JID.String() == msg.chatJID {
			m.msgScroll = m.maxMsgScroll(msg.chatJID)
		}
		return m, nil

	case tuiNewMsg:
		return m.applyNewMsg(msgEvent(msg)), listenForMsg()

	case tuiStatus:
		m.statusMsg = string(msg)
		m.statusTime = time.Now()
		return m, nil

	case tuiError:
		m.statusMsg = "Error: " + msg.err.Error()
		m.statusTime = time.Now()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) applyNewMsg(evt msgEvent) Model {
	// Also pull in any new messages that may have been added to the global map
	// by the history sync handler concurrently.
	m = m.rebuildMessages()
	key := evt.ChatJID.String()
	m.messages[key] = append(m.messages[key], evt.Message)

	found := false
	for i := range m.chats {
		if m.chats[i].JID.String() == key {
			found = true
			m.chats[i].LastMsg = evt.Message.Content
			m.chats[i].LastTime = evt.Message.Timestamp
			if !evt.Message.FromMe {
				m.chats[i].Unread++
			}
			break
		}
	}

	if !found {
		// New conversation – look it up in the global map.
		chatsMu.RLock()
		var name string
		var isGroup bool
		if chat, ok := chatsMap[key]; ok {
			name = chat.Name
			isGroup = chat.IsGroup
		} else {
			name = evt.ChatJID.User
			isGroup = evt.ChatJID.Server == types.GroupServer
		}
		chatsMu.RUnlock()

		newChat := ChatItem{
			JID:      evt.ChatJID,
			Name:     name,
			LastMsg:  evt.Message.Content,
			LastTime: evt.Message.Timestamp,
			IsGroup:  isGroup,
		}
		if !evt.Message.FromMe {
			newChat.Unread = 1
		}
		m.chats = append(m.chats, newChat)
	}

	// Auto-scroll messages if this chat is open.
	if m.selectedChat >= 0 && m.selectedChat < len(m.chats) &&
		m.chats[m.selectedChat].JID.String() == key {
		m.msgScroll = m.maxMsgScroll(key)
	}

	return m
}

// ── Key handling ──────────────────────────────────────────────────────────────

// rebuildFromGlobal replaces the model's chats and messages with the current
// contents of the global maps (called after a HistorySync event).
func (m Model) rebuildFromGlobal() Model {
	chatsMu.RLock()
	newChats := make([]ChatItem, 0, len(chatsMap))
	for _, c := range chatsMap {
		newChats = append(newChats, *c)
	}
	chatsMu.RUnlock()

	// Sort by last message time descending (most recent first), then name.
	sort.Slice(newChats, func(i, j int) bool {
		if !newChats[i].LastTime.Equal(newChats[j].LastTime) {
			return newChats[i].LastTime.After(newChats[j].LastTime)
		}
		return newChats[i].Name < newChats[j].Name
	})

	// Keep selected chat pointed at same JID if possible.
	var selectedJID string
	if m.selectedChat >= 0 && m.selectedChat < len(m.chats) {
		selectedJID = m.chats[m.selectedChat].JID.String()
	}
	newSelected := 0
	for i, c := range newChats {
		if c.JID.String() == selectedJID {
			newSelected = i
			break
		}
	}

	m.chats = newChats
	m.selectedChat = newSelected
	m.chatScroll = 0
	m = m.rebuildMessages()
	return m
}

// rebuildMessages copies the global message map into the model's local copy.
func (m Model) rebuildMessages() Model {
	messagesMu.RLock()
	for k, v := range messagesMap {
		cp := make([]Message, len(v))
		copy(cp, v)
		m.messages[k] = cp
	}
	messagesMu.RUnlock()
	return m
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.focus {
	case focusChatList:
		return m.keyChatList(msg)
	case focusMessages:
		return m.keyMessages(msg)
	case focusInput:
		return m.keyInput(msg)
	}
	return m, nil
}

func (m Model) keyChatList(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "j", "down":
		if m.selectedChat < len(m.chats)-1 {
			m.selectedChat++
			m.msgScroll = 0
			vis := m.visibleChatRows()
			if m.selectedChat >= m.chatScroll+vis {
				m.chatScroll = m.selectedChat - vis + 1
			}
		}

	case "k", "up":
		if m.selectedChat > 0 {
			m.selectedChat--
			m.msgScroll = 0
			if m.selectedChat < m.chatScroll {
				m.chatScroll = m.selectedChat
			}
		}

	case "enter":
		if len(m.chats) > 0 {
			m.focus = focusInput
			m.chats[m.selectedChat].Unread = 0
			key := m.chats[m.selectedChat].JID.String()
			m.msgScroll = m.maxMsgScroll(key)
			return m, loadChatMsgs(key)
		}

	case "tab":
		if len(m.chats) > 0 {
			m.focus = focusMessages
		}
	}
	return m, nil
}

func (m Model) keyMessages(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "q", "esc":
		m.focus = focusChatList

	case "tab", "i":
		m.focus = focusInput

	case "j", "down":
		if m.selectedChat >= 0 && m.selectedChat < len(m.chats) {
			mx := m.maxMsgScroll(m.chats[m.selectedChat].JID.String())
			if m.msgScroll < mx {
				m.msgScroll++
			}
		}

	case "k", "up":
		if m.msgScroll > 0 {
			m.msgScroll--
		}

	case "g":
		m.msgScroll = 0

	case "G":
		if m.selectedChat >= 0 && m.selectedChat < len(m.chats) {
			m.msgScroll = m.maxMsgScroll(m.chats[m.selectedChat].JID.String())
		}
	}
	return m, nil
}

func (m Model) keyInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "esc":
		m.focus = focusMessages

	case "tab":
		m.focus = focusChatList

	case "enter":
		if strings.TrimSpace(m.inputText) == "" ||
			m.selectedChat < 0 || m.selectedChat >= len(m.chats) {
			return m, nil
		}
		text := m.inputText
		jid := m.chats[m.selectedChat].JID
		m.inputText = ""
		m.inputCursor = 0
		return m, func() tea.Msg {
			if err := sendMessage(jid, text); err != nil {
				return tuiError{err}
			}
			return tuiStatus("Sent ✓")
		}

	case "backspace", "ctrl+h":
		if m.inputCursor > 0 {
			r := []rune(m.inputText)
			m.inputText = string(r[:m.inputCursor-1]) + string(r[m.inputCursor:])
			m.inputCursor--
		}

	case "ctrl+w": // delete word backwards
		if m.inputCursor > 0 {
			r := []rune(m.inputText)
			end := m.inputCursor
			for end > 0 && r[end-1] == ' ' {
				end--
			}
			for end > 0 && r[end-1] != ' ' {
				end--
			}
			m.inputText = string(r[:end]) + string(r[m.inputCursor:])
			m.inputCursor = end
		}

	case "left":
		if m.inputCursor > 0 {
			m.inputCursor--
		}

	case "right":
		if m.inputCursor < utf8.RuneCountInString(m.inputText) {
			m.inputCursor++
		}

	case "ctrl+a":
		m.inputCursor = 0

	case "ctrl+e":
		m.inputCursor = utf8.RuneCountInString(m.inputText)

	case "ctrl+k": // delete to end of line
		r := []rune(m.inputText)
		m.inputText = string(r[:m.inputCursor])

	case "ctrl+u": // delete to start of line
		r := []rune(m.inputText)
		m.inputText = string(r[m.inputCursor:])
		m.inputCursor = 0

	case " ": // space
		r := []rune(m.inputText)
		nr := make([]rune, 0, len(r)+1)
		nr = append(nr, r[:m.inputCursor]...)
		nr = append(nr, ' ')
		nr = append(nr, r[m.inputCursor:]...)
		m.inputText = string(nr)
		m.inputCursor++

	default:
		if k.Type == tea.KeyRunes {
			r := []rune(m.inputText)
			nr := make([]rune, 0, len(r)+len(k.Runes))
			nr = append(nr, r[:m.inputCursor]...)
			nr = append(nr, k.Runes...)
			nr = append(nr, r[m.inputCursor:]...)
			m.inputText = string(nr)
			m.inputCursor += len(k.Runes)
		}
	}
	return m, nil
}

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
	chatBox := chatBorder.Width(chatInner).Height(innerH).Render(chatContent)

	// ── Message panel ─────────────────────────────────────────────────────────
	msgContent := m.renderMessages(msgInner, innerH)
	msgBorder := sIdle
	if m.focus == focusMessages {
		msgBorder = sActive
	}
	msgBox := msgBorder.Width(msgInner).Height(innerH).Render(msgContent)

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

	return strings.Join(lines, "\n")
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
	messagesMu.RLock()
	msgs := make([]Message, len(messagesMap[key]))
	copy(msgs, messagesMap[key])
	messagesMu.RUnlock()

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

	return strings.Join(append(header, visible...), "\n")
}

func (m Model) formatMsg(msg Message, w int) []string {
	ts := sTime.Render(msg.Timestamp.Format("15:04"))
	var lines []string

	if msg.FromMe {
		meta := sTime.Render("You · ") + ts
		lines = append(lines, "  "+meta)
		for _, l := range wordWrap(msg.Content, w-4) {
			lines = append(lines, "  "+sMyMsg.Render(l))
		}
	} else {
		meta := sSender.Render(msg.Sender) + "  " + ts
		lines = append(lines, meta)
		for _, l := range wordWrap(msg.Content, w-4) {
			lines = append(lines, sTheirMsg.Render(l))
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
	flash := ""
	if m.statusMsg != "" && time.Since(m.statusTime) < 4*time.Second {
		flash = "   " + lipgloss.NewStyle().Foreground(clrText).Render(m.statusMsg)
	}
	keys := sTime.Render("  j/k navigate · g/G top/bottom · i type · q quit")
	return sStatus.Width(m.width).Render(conn + flash + keys)
}

// ── Dimension helpers ─────────────────────────────────────────────────────────

// visibleChatRows returns how many chat items fit in the list panel.
func (m Model) visibleChatRows() int {
	innerH := m.height - 7
	return max(1, innerH-2) // subtract the title + divider rows
}

// maxMsgScroll returns the maximum scroll offset for the given chat.
func (m Model) maxMsgScroll(key string) int {
	messagesMu.RLock()
	msgs := make([]Message, len(messagesMap[key]))
	copy(msgs, messagesMap[key])
	messagesMu.RUnlock()
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
func mergeMessages(a, b []Message) []Message {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]Message, 0, len(a)+len(b))
	for _, m := range append(append([]Message{}, a...), b...) {
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

// wordWrap splits text into lines of at most width runes, breaking at spaces.
func wordWrap(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var result []string
	r := []rune(text)
	for len(r) > 0 {
		if len(r) <= width {
			result = append(result, string(r))
			break
		}
		cut := width
		for cut > 0 && r[cut-1] != ' ' {
			cut--
		}
		if cut == 0 {
			cut = width
		}
		result = append(result, string(r[:cut]))
		r = r[cut:]
		for len(r) > 0 && r[0] == ' ' {
			r = r[1:]
		}
	}
	return result
}

// orDefault returns s if non-empty, otherwise def.
func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
