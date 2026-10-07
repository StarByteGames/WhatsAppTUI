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
	// PinnedAt is when the chat was pinned; zero if it is not pinned.
	PinnedAt time.Time
}

// Pinned reports whether the chat is pinned.
func (c ChatItem) Pinned() bool {
	return !c.PinnedAt.IsZero()
}

// Message is a single chat message stored in memory.
type Message struct {
	ID        string
	Sender    string
	SenderJID watypes.JID
	Content   string // current content (latest edit)
	Timestamp time.Time
	FromMe    bool
	ImagePath string // path to cached image file (empty if not an image)

	// Deleted is set when the sender revoked the message. The message is kept
	// in the history and rendered as deleted instead of being removed.
	Deleted   bool
	DeletedAt time.Time

	// EditedAt is when Content was last replaced by an edit (zero if never edited).
	EditedAt time.Time
	// Edits holds the previous versions of the message, oldest first.
	Edits []MessageVersion
}

// MessageVersion is one historic version of an edited message.
type MessageVersion struct {
	Content   string
	Timestamp time.Time // when this version was written
}

// VersionTime returns when the current content was written.
func (m Message) VersionTime() time.Time {
	if !m.EditedAt.IsZero() {
		return m.EditedAt
	}
	return m.Timestamp
}

// IsPlaceholder reports whether the message is a stand-in created for a revoke
// whose original content was never received.
func (m Message) IsPlaceholder() bool {
	return m.Content == "" && m.ImagePath == ""
}
