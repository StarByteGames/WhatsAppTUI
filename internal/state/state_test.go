package state

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/StarGames2025/Logger"
	watypes "go.mau.fi/whatsmeow/types"

	"DevStarByte/internal/types"
)

var testLogger *Logger.Logger

// The logger keeps its file open, so it is shared by all tests and created
// outside t.TempDir (which cannot remove open files on Windows).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "state-test")
	if err != nil {
		panic(err)
	}
	if testLogger, err = Logger.NewLogger(Logger.ERROR, filepath.Join(dir, "test.log"), false); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func newTestState(t *testing.T) *AppState {
	t.Helper()
	return New(nil, nil, testLogger)
}

var t0 = time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)

func msg(id, content string, ts time.Time) types.Message {
	return types.Message{ID: id, Content: content, Timestamp: ts, Sender: "Alice"}
}

func TestAddMessageDedupesAndSorts(t *testing.T) {
	s := newTestState(t)
	s.AddMessage("c", msg("2", "second", t0.Add(time.Minute)))
	s.AddMessage("c", msg("1", "first", t0))
	if _, isNew := s.AddMessage("c", msg("2", "second", t0.Add(time.Minute))); isNew {
		t.Fatal("duplicate reported as new")
	}
	got := s.Messages("c")
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "2" {
		t.Fatalf("unexpected messages: %+v", got)
	}
}

func TestEditKeepsHistory(t *testing.T) {
	s := newTestState(t)
	s.AddMessage("c", msg("1", "v1", t0))
	if !s.EditMessage("c", "1", "v2", t0.Add(time.Minute)) {
		t.Fatal("edit not applied")
	}
	if !s.EditMessage("c", "1", "v3", t0.Add(2*time.Minute)) {
		t.Fatal("second edit not applied")
	}
	// Re-delivery of an edit (e.g. by history sync) is ignored.
	if s.EditMessage("c", "1", "v2", t0.Add(time.Minute)) {
		t.Fatal("duplicate edit applied")
	}

	m := s.Messages("c")[0]
	if m.Content != "v3" || !m.EditedAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("wrong current version: %+v", m)
	}
	if len(m.Edits) != 2 || m.Edits[0].Content != "v1" || m.Edits[1].Content != "v2" {
		t.Fatalf("wrong history: %+v", m.Edits)
	}
	if !m.Edits[0].Timestamp.Equal(t0) {
		t.Fatalf("original version should keep its timestamp: %v", m.Edits[0].Timestamp)
	}
}

func TestOutOfOrderEdit(t *testing.T) {
	s := newTestState(t)
	s.AddMessage("c", msg("1", "v1", t0))
	s.EditMessage("c", "1", "v3", t0.Add(2*time.Minute))
	s.EditMessage("c", "1", "v2", t0.Add(time.Minute)) // arrives late

	m := s.Messages("c")[0]
	if m.Content != "v3" {
		t.Fatalf("late edit replaced newer content: %q", m.Content)
	}
	if len(m.Edits) != 2 || m.Edits[0].Content != "v1" || m.Edits[1].Content != "v2" {
		t.Fatalf("wrong history: %+v", m.Edits)
	}
}

func TestRedeliveredOriginalDoesNotUndoEdit(t *testing.T) {
	s := newTestState(t)
	s.AddMessage("c", msg("1", "v1", t0))
	s.EditMessage("c", "1", "v2", t0.Add(time.Minute))
	s.AddMessage("c", msg("1", "v1", t0)) // history sync delivers the original again
	if got := s.Messages("c")[0].Content; got != "v2" {
		t.Fatalf("edit was undone: %q", got)
	}
}

func TestRevokeKeepsMessage(t *testing.T) {
	s := newTestState(t)
	s.AddMessage("c", msg("1", "secret", t0))
	if !s.RevokeMessage("c", types.Message{ID: "1"}, t0.Add(time.Minute)) {
		t.Fatal("revoke not applied")
	}
	if s.RevokeMessage("c", types.Message{ID: "1"}, t0.Add(time.Minute)) {
		t.Fatal("second revoke reported a change")
	}
	got := s.Messages("c")
	if len(got) != 1 || !got[0].Deleted || got[0].Content != "secret" {
		t.Fatalf("deleted message not kept: %+v", got)
	}
	// A later re-delivery must not resurrect it.
	s.AddMessage("c", msg("1", "secret", t0))
	if !s.Messages("c")[0].Deleted {
		t.Fatal("re-delivery cleared the deleted flag")
	}
}

func TestRevokeOfUnknownMessageCreatesPlaceholder(t *testing.T) {
	s := newTestState(t)
	s.RevokeMessage("c", types.Message{ID: "1", Sender: "Bob", Timestamp: t0.Add(time.Hour)}, t0.Add(time.Hour))
	m := s.Messages("c")[0]
	if !m.Deleted || !m.IsPlaceholder() {
		t.Fatalf("expected deleted placeholder: %+v", m)
	}
	// The original arrives later (e.g. via history sync): content is adopted.
	s.AddMessage("c", msg("1", "hello", t0))
	m = s.Messages("c")[0]
	if !m.Deleted || m.Content != "hello" || !m.Timestamp.Equal(t0) {
		t.Fatalf("placeholder not merged: %+v", m)
	}
}

func TestChatUpdatesAndOrdering(t *testing.T) {
	s := newTestState(t)
	a := mustJID(t, "111@s.whatsapp.net")
	b := mustJID(t, "222@s.whatsapp.net")
	s.UpdateChat(ChatUpdate{JID: a, LastTime: t0, Unread: 1})
	s.UpdateChat(ChatUpdate{JID: b, Name: "Bob", LastTime: t0.Add(time.Minute), Unread: 2})
	s.UpdateChat(ChatUpdate{JID: a, Name: "Alice", LastTime: t0.Add(-time.Hour)}) // older: keeps last message

	list := s.ChatList()
	if len(list) != 2 || list[0].Name != "Bob" || list[1].Name != "Alice" {
		t.Fatalf("unexpected order: %+v", list)
	}
	if !list[1].LastTime.Equal(t0) {
		t.Fatal("older update replaced the last message")
	}
	if !s.MarkRead(b.String()) || s.MarkRead(b.String()) {
		t.Fatal("MarkRead should report a change exactly once")
	}
	// A real name is not overwritten by a later hint.
	s.UpdateChat(ChatUpdate{JID: b, Name: "Robert"})
	if c, _ := s.Chat(b.String()); c.Name != "Bob" {
		t.Fatalf("name overwritten: %q", c.Name)
	}
}

func mustJID(t *testing.T, s string) watypes.JID {
	t.Helper()
	jid, err := watypes.ParseJID(s)
	if err != nil {
		t.Fatal(err)
	}
	return jid
}

func TestMergeChatInto(t *testing.T) {
	s := newTestState(t)
	lid := mustJID(t, "164810914304158@lid")
	pn := mustJID(t, "491701234567@s.whatsapp.net")

	s.UpdateChat(ChatUpdate{JID: pn, Name: "Ruth", LastTime: t0, Unread: 1})
	s.AddMessage(pn.String(), msg("a", "via phone number", t0))
	s.UpdateChat(ChatUpdate{JID: lid, LastMsg: "via lid", LastTime: t0.Add(time.Hour), Unread: 2})
	s.AddMessage(lid.String(), msg("b", "via lid", t0.Add(time.Hour)))
	s.AddMessage(lid.String(), msg("a", "via phone number", t0)) // same message under both IDs

	s.MergeChatInto(lid, pn)

	if _, ok := s.Chat(lid.String()); ok || len(s.Messages(lid.String())) != 0 {
		t.Fatal("LID chat should be gone")
	}
	c, _ := s.Chat(pn.String())
	if c.Name != "Ruth" || c.Unread != 3 || c.LastMsg != "via lid" {
		t.Fatalf("chat not merged: %+v", c)
	}
	if got := s.Messages(pn.String()); len(got) != 2 || got[1].ID != "b" {
		t.Fatalf("messages not merged: %+v", got)
	}
}

func TestHasRealName(t *testing.T) {
	group := mustJID(t, "4915227678271-1377032213@g.us")
	cases := map[string]bool{"": false, "491701234567": false, group.User: false, "Ruth": true, "KjG 2026": true}
	for name, want := range cases {
		if got := HasRealName(types.ChatItem{JID: group, Name: name}); got != want {
			t.Errorf("HasRealName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSelfChatKeepsName(t *testing.T) {
	s := newTestState(t)
	self := mustJID(t, "491701234567@s.whatsapp.net")
	s.SetSelfChat(self, "Thomas (You)")
	if !s.IsSelfChat(self.String()) {
		t.Fatal("self chat not registered")
	}

	// Contact, push-name and DB updates must not rename it.
	s.SetChatName(self, "491701234567")
	s.MergeChat(types.ChatItem{JID: self, Name: "Thomas Mader"})
	s.UpdateChat(ChatUpdate{JID: self, Name: "Someone", LastMsg: "note", LastTime: t0})

	c, ok := s.Chat(self.String())
	if !ok || c.Name != "Thomas (You)" || c.LastMsg != "note" || c.Unread != 0 {
		t.Fatalf("unexpected self chat: %+v", c)
	}
}

func TestPinnedChatsFirst(t *testing.T) {
	s := newTestState(t)
	old := mustJID(t, "111@s.whatsapp.net")
	recent := mustJID(t, "222@s.whatsapp.net")
	pinA := mustJID(t, "333@s.whatsapp.net")
	pinB := mustJID(t, "444@s.whatsapp.net")
	s.UpdateChat(ChatUpdate{JID: old, Name: "Old", LastTime: t0})
	s.UpdateChat(ChatUpdate{JID: recent, Name: "Recent", LastTime: t0.Add(time.Hour)})
	s.UpdateChat(ChatUpdate{JID: pinA, Name: "PinA", LastTime: t0.Add(-time.Hour)})
	s.UpdateChat(ChatUpdate{JID: pinB, Name: "PinB", LastTime: t0.Add(-2 * time.Hour)})

	s.SetPinned(pinA, true, t0)
	s.SetPinned(pinB, true, t0.Add(time.Minute)) // pinned later: on top
	order := func() []string {
		var names []string
		for _, c := range s.ChatList() {
			names = append(names, c.Name)
		}
		return names
	}
	if got := fmt.Sprint(order()); got != "[PinB PinA Recent Old]" {
		t.Fatalf("unexpected order %s", got)
	}

	// Pinned without a known time (from stored settings): after timed pins.
	if !s.SetPinned(old, true, time.Time{}) || s.SetPinned(old, true, time.Time{}) {
		t.Fatal("SetPinned should report a change exactly once")
	}
	if got := fmt.Sprint(order()); got != "[PinB PinA Old Recent]" {
		t.Fatalf("unexpected order %s", got)
	}

	// Unpinning returns the chat to its place by recency.
	s.SetPinned(pinB, false, time.Time{})
	if got := fmt.Sprint(order()); got != "[PinA Old Recent PinB]" {
		t.Fatalf("unexpected order %s", got)
	}

	// A newer message moves an unpinned chat up, but never above pins.
	s.UpdateChat(ChatUpdate{JID: pinB, LastTime: t0.Add(2 * time.Hour)})
	if got := fmt.Sprint(order()); got != "[PinA Old PinB Recent]" {
		t.Fatalf("unexpected order %s", got)
	}
}

func TestEditBeforeMessageIsApplied(t *testing.T) {
	s := newTestState(t)
	// Edits arrive first (e.g. history sync is sent newest first).
	s.EditMessage("c", "1", "v3", t0.Add(2*time.Minute))
	s.EditMessage("c", "1", "v2", t0.Add(time.Minute))
	s.AddMessage("c", msg("1", "v1", t0))

	m := s.Messages("c")[0]
	if m.Content != "v3" || len(m.Edits) != 2 || m.Edits[0].Content != "v1" || m.Edits[1].Content != "v2" {
		t.Fatalf("deferred edits not applied: %+v", m)
	}
}

func TestMarkDeleted(t *testing.T) {
	s := newTestState(t)
	if s.MarkDeleted("c", "missing", t0) || len(s.Messages("c")) != 0 {
		t.Fatal("MarkDeleted must not create placeholders")
	}
	s.AddMessage("c", msg("1", "keep me", t0))
	if !s.MarkDeleted("c", "1", t0.Add(time.Minute)) || s.MarkDeleted("c", "1", t0.Add(time.Minute)) {
		t.Fatal("MarkDeleted should report a change exactly once")
	}
	if m := s.Messages("c")[0]; !m.Deleted || m.Content != "keep me" {
		t.Fatalf("message not kept as deleted: %+v", m)
	}
}
