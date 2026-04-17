package types

import (
	"time"

	watypes "go.mau.fi/whatsmeow/types"
)

// ChatItem is a sidebar entry representing one conversation.
type ChatItem struct {
	JID      watypes.JID
	Name     string
	LastMsg  string
	LastTime time.Time
	Unread   int
	IsGroup  bool
}

// Message is a single chat message stored in memory.
type Message struct {
	ID        string
	Sender    string
	SenderJID watypes.JID
	Content   string
	Timestamp time.Time
	FromMe    bool
	ImagePath string // path to cached image file (empty if not an image)
}

// MsgEvent carries an incoming message event to the TUI goroutine.
type MsgEvent struct {
	ChatJID watypes.JID
	Message Message
}
