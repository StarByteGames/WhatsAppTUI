package tui

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"go.mau.fi/whatsmeow/types"

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
)

// ── Model ─────────────────────────────────────────────────────────────────────

// Model is the bubbletea application model.
type Model struct {
	state         *state.AppState
	width, height int
	focus         focusArea

	// Chat list state.
	chats        []apptypes.ChatItem
	chatScroll   int
	selectedChat int

	// Message state.
	messages  map[string][]apptypes.Message
	msgScroll int

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
func NewModel(s *state.AppState, chats []apptypes.ChatItem) Model {
	msgs := make(map[string][]apptypes.Message)
	s.MessagesMu.RLock()
	for k, v := range s.MessagesMap {
		msgs[k] = append([]apptypes.Message{}, v...)
	}
	s.MessagesMu.RUnlock()

	return Model{
		state:    s,
		chats:    chats,
		messages: msgs,
	}
}

// ── Tea message types ─────────────────────────────────────────────────────────

type tuiNewMsg apptypes.MsgEvent
type tuiHistoryRefresh struct{}
type tuiLoadedMsgs struct {
	chatJID string
	msgs    []apptypes.Message
}
type tuiStatus string
type tuiError struct{ err error }
type tuiSyncCheck int // carries the syncCount at schedule time

// ── Init ──────────────────────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.listenForMsg(), m.listenForHistory())
}

// loadChatMsgs fetches persisted messages for a chat from SQLite.
func (m Model) loadChatMsgs(chatJID string) tea.Cmd {
	store := m.state.DB
	return func() tea.Msg {
		return tuiLoadedMsgs{chatJID: chatJID, msgs: store.LoadMessages(chatJID, 500)}
	}
}

// listenForHistory blocks until a history sync signal arrives, then delivers
// a tuiHistoryRefresh so the model can rebuild chats and messages from global state.
func (m Model) listenForHistory() tea.Cmd {
	ch := m.state.HistoryCh
	return func() tea.Msg {
		<-ch
		return tuiHistoryRefresh{}
	}
}

// listenForMsg blocks until a message arrives on incomingCh, then delivers it
// as a tuiNewMsg so the Update loop can process it.
func (m Model) listenForMsg() tea.Cmd {
	ch := m.state.IncomingCh
	return func() tea.Msg {
		return tuiNewMsg(<-ch)
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tuiHistoryRefresh:
		m.syncCount++
		m.syncDone = false
		snapshot := m.syncCount
		return m.rebuildFromGlobal(), tea.Batch(
			m.listenForHistory(),
			tea.Tick(8*time.Second, func(time.Time) tea.Msg { return tuiSyncCheck(snapshot) }),
		)

	case tuiLoadedMsgs:
		m.messages[msg.chatJID] = mergeMessages(m.messages[msg.chatJID], msg.msgs)
		if m.selectedChat >= 0 && m.selectedChat < len(m.chats) &&
			m.chats[m.selectedChat].JID.String() == msg.chatJID {
			m.msgScroll = m.maxMsgScroll(msg.chatJID)
		}
		return m, nil

	case tuiNewMsg:
		return m.applyNewMsg(apptypes.MsgEvent(msg)), m.listenForMsg()

	case tuiStatus:
		m.statusMsg = string(msg)
		m.statusTime = time.Now()
		return m, nil

	case tuiError:
		m.statusMsg = "Error: " + msg.err.Error()
		m.statusTime = time.Now()
		return m, nil

	case tuiSyncCheck:
		if int(msg) == m.syncCount {
			m.syncDone = true
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) applyNewMsg(evt apptypes.MsgEvent) Model {
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
		m.state.ChatsMu.RLock()
		var name string
		var isGroup bool
		if chat, ok := m.state.ChatsMap[key]; ok {
			name = chat.Name
			isGroup = chat.IsGroup
		} else {
			name = evt.ChatJID.User
			isGroup = evt.ChatJID.Server == types.GroupServer
		}
		m.state.ChatsMu.RUnlock()

		newChat := apptypes.ChatItem{
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
	m.state.ChatsMu.RLock()
	newChats := make([]apptypes.ChatItem, 0, len(m.state.ChatsMap))
	for _, c := range m.state.ChatsMap {
		newChats = append(newChats, *c)
	}
	m.state.ChatsMu.RUnlock()

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
	m.state.MessagesMu.RLock()
	for k, v := range m.state.MessagesMap {
		cp := make([]apptypes.Message, len(v))
		copy(cp, v)
		m.messages[k] = cp
	}
	m.state.MessagesMu.RUnlock()
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
			return m, m.loadChatMsgs(key)
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
		s := m.state
		m.inputText = ""
		m.inputCursor = 0
		return m, func() tea.Msg {
			if err := client.SendMessage(s, jid, text); err != nil {
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
