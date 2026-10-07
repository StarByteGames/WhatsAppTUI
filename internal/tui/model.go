package tui

import (
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"DevStarByte/internal/client"
	"DevStarByte/internal/state"
	apptypes "DevStarByte/internal/types"
)

// ── Focus areas ───────────────────────────────────────────────────────────────

type focusArea int

const (
	focusChatList focusArea = iota
	focusMessages
	focusInput
	focusSearch
)

// ── Model ─────────────────────────────────────────────────────────────────────

// Model is the bubbletea application model. Messages are not copied into the
// model: they are read from the shared state when rendering.
type Model struct {
	state         *state.AppState
	width, height int
	focus         focusArea

	// Chat list state: a sorted snapshot of the shared chat map, filtered by
	// the search query.
	chats        []apptypes.ChatItem
	chatScroll   int
	selectedChat int
	search       string

	// Message panel state. msgScroll < 0 means "stick to the bottom".
	msgScroll int
	showEdits bool

	// Text input state.
	inputText   string
	inputCursor int // rune index

	// Sync status.
	syncCount int
	syncDone  bool

	// Temporary status flash.
	statusMsg  string
	statusTime time.Time
}

// NewModel creates an initialised Model.
func NewModel(s *state.AppState) Model {
	return Model{
		state:     s,
		chats:     s.ChatList(),
		msgScroll: -1,
	}
}

// ── Tea message types ─────────────────────────────────────────────────────────

type tuiUpdate struct{}
type tuiHistorySync struct{}
type tuiStatus string
type tuiError struct{ err error }
type tuiSyncCheck int // carries the syncCount at schedule time

// ── Init ──────────────────────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.listenForUpdates(), m.listenForHistory())
}

// listenForUpdates blocks until the shared state changes.
func (m Model) listenForUpdates() tea.Cmd {
	ch := m.state.UpdateCh
	return func() tea.Msg {
		<-ch
		return tuiUpdate{}
	}
}

// listenForHistory blocks until a history sync batch was processed.
func (m Model) listenForHistory() tea.Cmd {
	ch := m.state.HistoryCh
	return func() tea.Msg {
		<-ch
		return tuiHistorySync{}
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureChatVisible()
		return m, nil

	case tuiUpdate:
		return m.refreshChats(), m.listenForUpdates()

	case tuiHistorySync:
		// History arrives in batches; consider the sync done once no new
		// batch arrived for a while.
		m.syncCount++
		m.syncDone = false
		snapshot := m.syncCount
		return m, tea.Batch(
			m.listenForHistory(),
			tea.Tick(8*time.Second, func(time.Time) tea.Msg { return tuiSyncCheck(snapshot) }),
		)

	case tuiSyncCheck:
		if int(msg) == m.syncCount {
			m.syncDone = true
		}
		return m, nil

	case tuiStatus:
		return m.flash(string(msg)), nil

	case tuiError:
		return m.flash("Error: " + msg.err.Error()), nil

	case tea.MouseMsg:
		return m.handleMouse(msg), nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) flash(text string) Model {
	m.statusMsg = text
	m.statusTime = time.Now()
	return m
}

// selectedKey returns the JID string of the selected chat, or "".
func (m Model) selectedKey() string {
	if m.selectedChat >= 0 && m.selectedChat < len(m.chats) {
		return m.chats[m.selectedChat].JID.String()
	}
	return ""
}

// refreshChats re-reads the chat list from the shared state and applies the
// search filter, keeping the selection on the same chat even if the order
// changed.
func (m Model) refreshChats() Model {
	selected := m.selectedKey()
	m.chats = filterChats(m.state.ChatList(), m.search)
	m.selectedChat = min(m.selectedChat, max(0, len(m.chats)-1))
	for i, c := range m.chats {
		if c.JID.String() == selected {
			m.selectedChat = i
			break
		}
	}
	m.ensureChatVisible()
	if m.focus == focusMessages || m.focus == focusInput {
		m = m.markSelectedRead()
	}
	return m
}

// filterChats keeps the chats whose display name or number contains every
// word of the query (case-insensitive).
func filterChats(chats []apptypes.ChatItem, query string) []apptypes.ChatItem {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return chats
	}
	out := chats[:0:0]
	for _, c := range chats {
		haystack := strings.ToLower(displayName(c) + " " + c.Name + " " + c.JID.User)
		match := true
		for _, t := range terms {
			if !strings.Contains(haystack, t) {
				match = false
				break
			}
		}
		if match {
			out = append(out, c)
		}
	}
	return out
}

// setSearch changes the search query and re-filters the chat list, selecting
// the best (first) match.
func (m Model) setSearch(query string) Model {
	m.search = query
	m.selectedChat = 0
	m.chatScroll = 0
	m.msgScroll = -1
	m.chats = filterChats(m.state.ChatList(), query)
	return m
}

// markSelectedRead clears the unread counter of the open chat.
func (m Model) markSelectedRead() Model {
	if key := m.selectedKey(); key != "" {
		m.state.MarkRead(key)
		m.chats[m.selectedChat].Unread = 0
	}
	return m
}

// ensureChatVisible scrolls the chat list so the selection is on screen.
func (m *Model) ensureChatVisible() {
	vis := m.layout().chatRows
	if m.selectedChat < m.chatScroll {
		m.chatScroll = m.selectedChat
	} else if m.selectedChat >= m.chatScroll+vis {
		m.chatScroll = m.selectedChat - vis + 1
	}
	m.chatScroll = max(0, min(m.chatScroll, len(m.chats)-vis))
}

func (m Model) selectChat(i int) Model {
	if i < 0 || i >= len(m.chats) || i == m.selectedChat {
		return m
	}
	m.selectedChat = i
	m.msgScroll = -1
	m.ensureChatVisible()
	return m
}

// ── Scrolling ─────────────────────────────────────────────────────────────────

// scrollMessages moves the message view by delta lines (negative = up).
func (m Model) scrollMessages(delta int) Model {
	mx := m.maxMsgScroll()
	pos := m.msgScroll
	if pos < 0 || pos > mx {
		pos = mx
	}
	pos += delta
	if pos >= mx {
		m.msgScroll = -1 // back at the bottom: follow new messages again
	} else {
		m.msgScroll = max(0, pos)
	}
	return m
}

func (m Model) handleMouse(msg tea.MouseMsg) Model {
	var delta int
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		delta = -3
	case tea.MouseButtonWheelDown:
		delta = 3
	default:
		return m
	}
	if msg.X < m.layout().chatInner+2 { // over the chat list
		return m.selectChat(m.selectedChat + delta/3)
	}
	return m.scrollMessages(delta)
}

// ── Key handling ──────────────────────────────────────────────────────────────

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if msg.String() == "ctrl+f" {
		m.focus = focusSearch
		return m, nil
	}
	switch m.focus {
	case focusSearch:
		return m.keySearch(msg)
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
	case "q":
		return m, tea.Quit
	case "/":
		m.focus = focusSearch
	case "esc":
		if m.search != "" {
			m = m.setSearch("")
		}
	case "j", "down":
		m = m.selectChat(m.selectedChat + 1)
	case "k", "up":
		m = m.selectChat(m.selectedChat - 1)
	case "pgdown":
		m = m.selectChat(min(len(m.chats)-1, m.selectedChat+m.layout().chatRows))
	case "pgup":
		m = m.selectChat(max(0, m.selectedChat-m.layout().chatRows))
	case "home", "g":
		m = m.selectChat(0)
	case "end", "G":
		m = m.selectChat(len(m.chats) - 1)
	case "enter":
		if len(m.chats) > 0 {
			m.focus = focusInput
			m.msgScroll = -1
			m = m.markSelectedRead()
		}
	case "tab":
		if len(m.chats) > 0 {
			m.focus = focusMessages
			m = m.markSelectedRead()
		}
	}
	return m, nil
}

// keySearch handles typing in the chat search bar. The list is filtered as
// you type; Enter opens the selected match.
func (m Model) keySearch(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	r := []rune(m.search)
	switch k.String() {
	case "esc":
		m.focus = focusChatList
		m = m.setSearch("")
	case "tab":
		m.focus = focusChatList // keep the filter
	case "enter":
		if len(m.chats) > 0 {
			m.focus = focusInput
			m = m.markSelectedRead()
		}
	case "down", "ctrl+n", "ctrl+j":
		m = m.selectChat(m.selectedChat + 1)
	case "up", "ctrl+p", "ctrl+k":
		m = m.selectChat(m.selectedChat - 1)
	case "backspace", "ctrl+h":
		if len(r) > 0 {
			m = m.setSearch(string(r[:len(r)-1]))
		}
	case "ctrl+w", "ctrl+u":
		m = m.setSearch("")
	default:
		switch k.Type {
		case tea.KeySpace:
			m = m.setSearch(m.search + " ")
		case tea.KeyRunes:
			m = m.setSearch(m.search + strings.NewReplacer("\n", " ", "\r", "").Replace(string(k.Runes)))
		}
	}
	return m, nil
}

func (m Model) keyMessages(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	page := m.layout().msgRows
	switch k.String() {
	case "q", "esc":
		m.focus = focusChatList
	case "tab", "i":
		m.focus = focusInput
	case "j", "down":
		m = m.scrollMessages(1)
	case "k", "up":
		m = m.scrollMessages(-1)
	case "pgdown", "ctrl+d", " ":
		m = m.scrollMessages(page)
	case "pgup", "ctrl+u":
		m = m.scrollMessages(-page)
	case "g", "home":
		m.msgScroll = 0
	case "G", "end":
		m.msgScroll = -1
	case "e":
		m.showEdits = !m.showEdits
		if m.showEdits {
			m = m.flash("Showing edit history")
		} else {
			m = m.flash("Edit history hidden")
		}
	}
	return m, nil
}

func (m Model) keyInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	r := []rune(m.inputText)
	switch k.String() {
	case "esc":
		m.focus = focusMessages

	case "tab":
		m.focus = focusChatList

	case "enter":
		key := m.selectedKey()
		if strings.TrimSpace(m.inputText) == "" || key == "" {
			return m, nil
		}
		text := m.inputText
		jid := m.chats[m.selectedChat].JID
		s := m.state
		m.inputText = ""
		m.inputCursor = 0
		m.msgScroll = -1
		return m, func() tea.Msg {
			if err := client.SendMessage(s, jid, text); err != nil {
				return tuiError{err}
			}
			return tuiStatus("Sent ✓")
		}

	case "pgup":
		m = m.scrollMessages(-m.layout().msgRows)
	case "pgdown":
		m = m.scrollMessages(m.layout().msgRows)

	case "backspace", "ctrl+h":
		if m.inputCursor > 0 {
			m.setInput(string(r[:m.inputCursor-1])+string(r[m.inputCursor:]), m.inputCursor-1)
		}

	case "delete":
		if m.inputCursor < len(r) {
			m.setInput(string(r[:m.inputCursor])+string(r[m.inputCursor+1:]), m.inputCursor)
		}

	case "ctrl+w": // delete word backwards
		end := m.inputCursor
		for end > 0 && r[end-1] == ' ' {
			end--
		}
		for end > 0 && r[end-1] != ' ' {
			end--
		}
		m.setInput(string(r[:end])+string(r[m.inputCursor:]), end)

	case "left":
		m.inputCursor = max(0, m.inputCursor-1)
	case "right":
		m.inputCursor = min(len(r), m.inputCursor+1)
	case "ctrl+a", "home":
		m.inputCursor = 0
	case "ctrl+e", "end":
		m.inputCursor = len(r)

	case "ctrl+k": // delete to end of line
		m.setInput(string(r[:m.inputCursor]), m.inputCursor)
	case "ctrl+u": // delete to start of line
		m.setInput(string(r[m.inputCursor:]), 0)

	default:
		switch k.Type {
		case tea.KeySpace:
			m.insert([]rune{' '})
		case tea.KeyRunes:
			m.insert(k.Runes)
		}
	}
	return m, nil
}

func (m *Model) setInput(text string, cursor int) {
	m.inputText = text
	m.inputCursor = max(0, min(cursor, utf8.RuneCountInString(text)))
}

// insert inserts runes at the cursor. Newlines from pastes become spaces since
// the input is a single line.
func (m *Model) insert(in []rune) {
	clean := make([]rune, 0, len(in))
	for _, c := range in {
		switch c {
		case '\r':
			continue
		case '\n', '\t':
			c = ' '
		}
		clean = append(clean, c)
	}
	r := []rune(m.inputText)
	nr := make([]rune, 0, len(r)+len(clean))
	nr = append(nr, r[:m.inputCursor]...)
	nr = append(nr, clean...)
	nr = append(nr, r[m.inputCursor:]...)
	m.setInput(string(nr), m.inputCursor+len(clean))
}
