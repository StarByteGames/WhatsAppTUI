package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/StarGames2025/Logger"
	tea "github.com/charmbracelet/bubbletea"
	"go.mau.fi/whatsmeow/types"

	"DevStarByte/internal/state"
	apptypes "DevStarByte/internal/types"
)

var testLogger *Logger.Logger

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tui-test")
	if err != nil {
		panic(err)
	}
	if testLogger, err = Logger.NewLogger(Logger.ERROR, filepath.Join(dir, "test.log"), false); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

var (
	chatJID = types.NewJID("491701234567", types.DefaultUserServer)
	t0      = time.Date(2026, 5, 6, 9, 0, 0, 0, time.UTC)
)

func newTestModel(t *testing.T, msgs ...apptypes.Message) Model {
	t.Helper()
	s := state.New(nil, nil, testLogger)
	s.UpdateChat(state.ChatUpdate{JID: chatJID, Name: "Alice", LastTime: t0})
	for _, m := range msgs {
		s.AddMessage(chatJID.String(), m)
	}
	m := NewModel(s)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return updated.(Model)
}

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	return m
}

func TestDeletedMessageIsShown(t *testing.T) {
	m := newTestModel(t,
		apptypes.Message{ID: "1", Sender: "Alice", Content: "you will not see this", Timestamp: t0, Deleted: true},
		apptypes.Message{ID: "2", Sender: "Alice", Timestamp: t0.Add(time.Minute), Deleted: true},
	)
	view := m.View()
	for _, want := range []string{"you will not see this", "· deleted", "This message was deleted"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
}

func TestEditHistoryToggle(t *testing.T) {
	m := newTestModel(t, apptypes.Message{
		ID: "1", Sender: "Alice", Content: "final text", Timestamp: t0,
		EditedAt: t0.Add(2 * time.Minute),
		Edits: []apptypes.MessageVersion{
			{Content: "first draft", Timestamp: t0},
			{Content: "second draft", Timestamp: t0.Add(time.Minute)},
		},
	})
	view := m.View()
	if !strings.Contains(view, "edited (2, e to show)") || strings.Contains(view, "first draft") {
		t.Fatalf("history should be collapsed by default:\n%s", view)
	}

	m = press(m, "tab", "e") // focus messages, toggle
	view = m.View()
	for _, want := range []string{"first draft", "second draft", "final text", "✎ 09:01"} {
		if !strings.Contains(view, want) {
			t.Errorf("expanded view is missing %q:\n%s", want, view)
		}
	}
	if strings.Index(view, "first draft") > strings.Index(view, "final text") {
		t.Error("previous versions should be listed before the current one")
	}
}

func TestScrollUpFromBottom(t *testing.T) {
	var msgs []apptypes.Message
	for i := range 50 {
		msgs = append(msgs, apptypes.Message{
			ID: fmt.Sprint(i), Sender: "Alice", Content: fmt.Sprintf("message %d", i),
			Timestamp: t0.Add(time.Duration(i) * time.Minute),
		})
	}
	m := press(newTestModel(t, msgs...), "tab")
	if m.msgScroll != -1 || !strings.Contains(m.View(), "message 49") {
		t.Fatal("should start at the bottom")
	}

	m = press(m, "k")
	if m.msgScroll != m.maxMsgScroll()-1 {
		t.Fatalf("k at the bottom should scroll up by one line, got offset %d (max %d)", m.msgScroll, m.maxMsgScroll())
	}
	m = press(m, "j")
	if m.msgScroll != -1 {
		t.Fatalf("returning to the bottom should re-enable follow mode, got %d", m.msgScroll)
	}
	m = press(m, "g")
	if view := m.View(); !strings.Contains(view, "message 0") || !strings.Contains(view, "more lines") {
		t.Fatalf("g should jump to the top:\n%s", view)
	}
}

func TestSelectionFollowsChatOnReorder(t *testing.T) {
	m := newTestModel(t)
	other := types.NewJID("491709999999", types.DefaultUserServer)
	m.state.UpdateChat(state.ChatUpdate{JID: other, Name: "Bob", LastTime: t0.Add(-time.Hour)})
	updated, _ := m.Update(tuiUpdate{})
	m = updated.(Model)
	m = press(m, "down") // select Bob
	if m.selectedKey() != other.String() {
		t.Fatalf("expected Bob to be selected, got %s", m.selectedKey())
	}

	// Bob gets a new message and moves to the top: selection must follow.
	m.state.UpdateChat(state.ChatUpdate{JID: other, LastTime: t0.Add(time.Hour), Unread: 1})
	updated, _ = m.Update(tuiUpdate{})
	m = updated.(Model)
	if m.selectedChat != 0 || m.selectedKey() != other.String() {
		t.Fatalf("selection did not follow the chat: index %d key %s", m.selectedChat, m.selectedKey())
	}
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		jid, name, want string
	}{
		{"491701234567@s.whatsapp.net", "Ruth", "Ruth"},
		{"491701234567@s.whatsapp.net", "491701234567", "+491701234567"},
		{"164810914304158@lid", "164810914304158", "Unknown contact"},
		{"4915227678271-1377032213@g.us", "4915227678271-1377032213", "Unnamed group"},
	}
	for _, tc := range cases {
		jid, _ := types.ParseJID(tc.jid)
		c := apptypes.ChatItem{JID: jid, Name: tc.name, IsGroup: jid.Server == types.GroupServer}
		if got := displayName(c); got != tc.want {
			t.Errorf("displayName(%s, %q) = %q, want %q", tc.jid, tc.name, got, tc.want)
		}
	}
}

func TestChatSearch(t *testing.T) {
	m := newTestModel(t) // has "Alice"
	for i, name := range []string{"KjG Sommerzeltlager 2026", "KjG-Gruppe 2026", "Familie Mader"} {
		m.state.UpdateChat(state.ChatUpdate{
			JID:      types.NewJID(fmt.Sprint(4917000000+i), types.DefaultUserServer),
			Name:     name,
			LastTime: t0.Add(time.Duration(i) * time.Minute),
		})
	}
	updated, _ := m.Update(tuiUpdate{})
	m = updated.(Model)

	m = press(m, "/", "k", "J", "g", " ", "2026")
	if m.focus != focusSearch || len(m.chats) != 2 {
		t.Fatalf("expected 2 KjG matches, got %d: %+v", len(m.chats), m.chats)
	}
	view := m.View()
	if !strings.Contains(view, "/ kJg 2026") || strings.Contains(view, "Familie Mader") {
		t.Fatalf("search bar or filter not rendered:\n%s", view)
	}

	m = press(m, "down")
	want := m.chats[1].JID
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.focus != focusInput || m.chats[m.selectedChat].JID != want {
		t.Fatal("Enter should open the selected match")
	}

	// Esc in the chat list clears the filter.
	m.focus = focusChatList
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.search != "" || len(m.chats) != 4 {
		t.Fatalf("filter not cleared: %q, %d chats", m.search, len(m.chats))
	}

	// Matching by phone number works too.
	m = press(m, "/", "4917000000")
	if len(m.chats) != 1 || m.chats[0].Name != "KjG Sommerzeltlager 2026" {
		t.Fatalf("number search failed: %+v", m.chats)
	}
}
