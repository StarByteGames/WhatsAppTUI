package client

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/StarGames2025/Logger"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"DevStarByte/internal/state"
)

var testLogger *Logger.Logger

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "client-test")
	if err != nil {
		panic(err)
	}
	if testLogger, err = Logger.NewLogger(Logger.ERROR, filepath.Join(dir, "test.log"), false); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

var (
	chat  = types.NewJID("491701234567", types.DefaultUserServer)
	start = time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
)

func event(id string, ts time.Time, m *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            id,
			PushName:      "Alice",
			Timestamp:     ts,
		},
		Message: m,
	}
}

func protocol(t waE2E.ProtocolMessage_Type, target string, edited *waE2E.Message) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          t.Enum(),
		Key:           &waCommon.MessageKey{ID: proto.String(target)},
		EditedMessage: edited,
	}}
}

func TestLiveEditAndRevoke(t *testing.T) {
	s := state.New(nil, nil, testLogger)
	key := chat.String()

	handleLiveMessage(s, event("A", start, &waE2E.Message{Conversation: proto.String("hello")}))
	handleLiveMessage(s, event("E1", start.Add(time.Minute),
		protocol(waE2E.ProtocolMessage_MESSAGE_EDIT, "A", &waE2E.Message{Conversation: proto.String("hello, world")})))
	// Edits may also arrive wrapped in an EditedMessage.
	handleLiveMessage(s, event("E2", start.Add(2*time.Minute), &waE2E.Message{EditedMessage: &waE2E.FutureProofMessage{
		Message: protocol(waE2E.ProtocolMessage_MESSAGE_EDIT, "A", &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("hello, everyone")},
		}),
	}}))

	msgs := s.Messages(key)
	if len(msgs) != 1 {
		t.Fatalf("protocol messages must not show up as messages: %+v", msgs)
	}
	if msgs[0].Content != "hello, everyone" || len(msgs[0].Edits) != 2 {
		t.Fatalf("edits not applied: %+v", msgs[0])
	}

	handleLiveMessage(s, event("R", start.Add(3*time.Minute), protocol(waE2E.ProtocolMessage_REVOKE, "A", nil)))
	msgs = s.Messages(key)
	if len(msgs) != 1 || !msgs[0].Deleted || msgs[0].Content != "hello, everyone" {
		t.Fatalf("revoked message must be kept and flagged: %+v", msgs)
	}

	c, ok := s.Chat(key)
	if !ok || c.Name != "Alice" || c.Unread != 1 {
		t.Fatalf("unexpected chat entry: %+v", c)
	}
}

func TestRevokeOfUnknownMessage(t *testing.T) {
	s := state.New(nil, nil, testLogger)
	handleLiveMessage(s, event("R", start, protocol(waE2E.ProtocolMessage_REVOKE, "missing", nil)))
	msgs := s.Messages(chat.String())
	if len(msgs) != 1 || msgs[0].ID != "missing" || !msgs[0].Deleted || msgs[0].Sender != "Alice" {
		t.Fatalf("expected a deleted placeholder: %+v", msgs)
	}
}

func TestExtractMsgContent(t *testing.T) {
	cases := []struct {
		name string
		msg  *waE2E.Message
		want string
	}{
		{"text", &waE2E.Message{Conversation: proto.String("hi")}, "hi"},
		{"ephemeral image", &waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("cat")}},
		}}, "[Image: cat]"},
		{"reaction", &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍")}}, "[Reaction: 👍]"},
		{"removed reaction", &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("")}}, ""},
		{"protocol", protocol(waE2E.ProtocolMessage_REVOKE, "x", nil), ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		if got := extractMsgContent(tc.msg); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func webMsg(id string, ts time.Time, m *waE2E.Message) *waHistorySync.HistorySyncMsg {
	return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
		Key:              &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), ID: proto.String(id), FromMe: proto.Bool(false)},
		MessageTimestamp: proto.Uint64(uint64(ts.Unix())),
		PushName:         proto.String("Alice"),
		Message:          m,
	}}
}

func TestHistoryNewestFirstKeepsEditsAndDeletes(t *testing.T) {
	s := state.New(nil, nil, testLogger)
	conv := &waHistorySync.Conversation{
		ID: proto.String(chat.String()),
		// Newest first, like WhatsApp sends it.
		Messages: []*waHistorySync.HistorySyncMsg{
			webMsg("R", start.Add(3*time.Minute), protocol(waE2E.ProtocolMessage_REVOKE, "B", nil)),
			webMsg("E", start.Add(2*time.Minute), protocol(waE2E.ProtocolMessage_MESSAGE_EDIT, "A", &waE2E.Message{Conversation: proto.String("edited")})),
			webMsg("B", start.Add(time.Minute), &waE2E.Message{Conversation: proto.String("to be deleted")}),
			webMsg("A", start, &waE2E.Message{Conversation: proto.String("original")}),
		},
	}
	processHistoryConversation(s, conv, nil)

	msgs := s.Messages(chat.String())
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %+v", msgs)
	}
	if a := msgs[0]; a.Content != "edited" || len(a.Edits) != 1 || a.Edits[0].Content != "original" {
		t.Fatalf("edit history lost: %+v", a)
	}
	if b := msgs[1]; !b.Deleted || b.Content != "to be deleted" {
		t.Fatalf("deleted message not kept: %+v", b)
	}
}
