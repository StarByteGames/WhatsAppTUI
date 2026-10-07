package state

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/StarGames2025/Logger"
	"go.mau.fi/whatsmeow"
	watypes "go.mau.fi/whatsmeow/types"

	"DevStarByte/internal/db"
	"DevStarByte/internal/types"
)

// AppState holds all shared runtime state that is accessed by both the
// WhatsApp client event handlers and the TUI. Messages and chats must only be
// changed through its methods, which keep memory and the database in sync.
type AppState struct {
	Client *whatsmeow.Client
	DB     *db.Store
	Logger *Logger.Logger

	chatsMu sync.RWMutex
	chats   map[string]*types.ChatItem
	selfKey string // chat with the user's own number ("message yourself")

	messagesMu   sync.RWMutex
	messages     map[string][]types.Message
	pendingEdits map[pendingKey][]types.MessageVersion // edits for messages not received yet

	// UpdateCh signals the TUI that chats or messages changed and the view
	// should be refreshed. It is coalescing: one pending signal is enough.
	UpdateCh chan struct{}
	// HistoryCh receives one signal per processed history sync batch.
	HistoryCh chan struct{}

	connected atomic.Bool
}

// New creates a new AppState with the given dependencies.
func New(client *whatsmeow.Client, store *db.Store, logger *Logger.Logger) *AppState {
	return &AppState{
		Client:       client,
		DB:           store,
		Logger:       logger,
		chats:        make(map[string]*types.ChatItem),
		messages:     make(map[string][]types.Message),
		pendingEdits: make(map[pendingKey][]types.MessageVersion),
		UpdateCh:     make(chan struct{}, 1),
		HistoryCh:    make(chan struct{}, 8),
	}
}

// Notify tells the TUI that state has changed. It never blocks.
func (s *AppState) Notify() {
	select {
	case s.UpdateCh <- struct{}{}:
	default:
	}
}

// NotifyHistory signals that a history sync batch was processed. It never blocks.
func (s *AppState) NotifyHistory() {
	select {
	case s.HistoryCh <- struct{}{}:
	default:
	}
	s.Notify()
}

// SetConnected records the connection state and refreshes the TUI.
func (s *AppState) SetConnected(c bool) {
	s.connected.Store(c)
	s.Notify()
}

// Connected reports whether the client is currently connected.
func (s *AppState) Connected() bool {
	return s.connected.Load()
}

// ── Messages ──────────────────────────────────────────────────────────────────

// Messages returns a copy of the messages of a chat, oldest first.
func (s *AppState) Messages(chatKey string) []types.Message {
	s.messagesMu.RLock()
	defer s.messagesMu.RUnlock()
	return append([]types.Message(nil), s.messages[chatKey]...)
}

// LoadPersisted seeds memory with messages loaded from the database without
// writing them back.
func (s *AppState) LoadPersisted(all map[string][]types.Message) {
	s.messagesMu.Lock()
	defer s.messagesMu.Unlock()
	for key, msgs := range all {
		for _, m := range msgs {
			s.upsertLocked(key, m)
		}
	}
}

// AddMessage stores a message in memory and in the database. If a message
// with the same ID already exists the two are merged. It returns the stored
// message and whether it was new.
func (s *AppState) AddMessage(chatKey string, msg types.Message) (types.Message, bool) {
	s.messagesMu.Lock()
	merged, isNew := s.upsertLocked(chatKey, msg)
	// Apply edits that arrived before the message.
	key := pendingKey{chatKey, msg.ID}
	pending := s.pendingEdits[key]
	delete(s.pendingEdits, key)
	var saved []savedEdit
	if len(pending) > 0 {
		msgs := s.messages[chatKey]
		i := indexOf(msgs, msg.ID)
		for _, p := range pending {
			if e, ok := applyEditLocked(&msgs[i], p.Content, p.Timestamp); ok {
				saved = append(saved, e)
			}
		}
		merged = msgs[i]
	}
	s.messagesMu.Unlock()

	s.DB.PersistMessage(chatKey, merged)
	for _, e := range saved {
		// Every write sets the final content, so the order does not matter.
		s.DB.SaveEdit(chatKey, msg.ID, e.prev, merged.Content, merged.EditedAt, e.current)
	}
	return merged, isNew
}

// upsertLocked inserts msg sorted by time, or merges it into the existing
// message with the same ID. Caller must hold messagesMu.
func (s *AppState) upsertLocked(chatKey string, msg types.Message) (types.Message, bool) {
	msgs := s.messages[chatKey]
	if i := indexOf(msgs, msg.ID); i >= 0 {
		msgs[i] = mergeMessage(msgs[i], msg)
		if msg.Timestamp != msgs[i].Timestamp {
			sortMessages(msgs)
			i = indexOf(msgs, msg.ID)
		}
		return msgs[i], false
	}
	// Insert keeping chronological order (usually an append).
	pos := sort.Search(len(msgs), func(i int) bool { return msgs[i].Timestamp.After(msg.Timestamp) })
	msgs = append(msgs, types.Message{})
	copy(msgs[pos+1:], msgs[pos:])
	msgs[pos] = msg
	s.messages[chatKey] = msgs
	return msg, true
}

// mergeMessage combines a re-delivered copy of a message with the stored one.
// Deletions and edits are sticky; a revoke placeholder adopts real content.
func mergeMessage(old, in types.Message) types.Message {
	out := old
	if old.IsPlaceholder() && !in.IsPlaceholder() {
		out.Content = in.Content
		out.Timestamp = in.Timestamp
	} else if old.EditedAt.IsZero() && in.Content != "" {
		out.Content = in.Content
	}
	if in.ImagePath != "" {
		out.ImagePath = in.ImagePath
	}
	if in.Sender != "" && (out.Sender == "" || out.Sender == out.SenderJID.User) {
		out.Sender = in.Sender
	}
	if out.SenderJID.IsEmpty() {
		out.SenderJID = in.SenderJID
	}
	if in.Deleted && !out.Deleted {
		out.Deleted = true
		out.DeletedAt = in.DeletedAt
	}
	if out.EditedAt.IsZero() && !in.EditedAt.IsZero() {
		out.EditedAt = in.EditedAt
		out.Edits = in.Edits
	}
	return out
}

// EditMessage applies an edit to a message. Duplicate and out-of-order edits
// are handled: the newest version becomes the content, all others are kept as
// history. It reports whether anything changed.
func (s *AppState) EditMessage(chatKey, msgID, content string, at time.Time) bool {
	if content == "" {
		return false
	}
	s.messagesMu.Lock()
	msgs := s.messages[chatKey]
	i := indexOf(msgs, msgID)
	if i < 0 {
		// The edit arrived before the message itself (history syncs are sent
		// newest first, live messages can be reordered): apply it later.
		key := pendingKey{chatKey, msgID}
		s.pendingEdits[key] = append(s.pendingEdits[key], types.MessageVersion{Content: content, Timestamp: at})
		s.messagesMu.Unlock()
		s.Logger.Debug("Edit for unknown message " + msgID + " in " + chatKey + " deferred")
		return false
	}
	saved, ok := applyEditLocked(&msgs[i], content, at)
	s.messagesMu.Unlock()

	if ok {
		s.DB.SaveEdit(chatKey, msgID, saved.prev, content, at, saved.current)
	}
	return ok
}

type pendingKey struct{ chat, id string }

type savedEdit struct {
	prev    types.MessageVersion // version to add to the history
	current bool                 // whether the edit became the current content
}

// applyEditLocked applies an edit to m. Caller must hold messagesMu.
func applyEditLocked(m *types.Message, content string, at time.Time) (savedEdit, bool) {
	if content == m.Content || hasVersion(m.Edits, content, at) {
		return savedEdit{}, false
	}
	var e savedEdit
	e.current = at.After(m.VersionTime())
	if e.current {
		e.prev = types.MessageVersion{Content: m.Content, Timestamp: m.VersionTime()}
		m.Content = content
		m.EditedAt = at
	} else {
		// An older edit arriving late only extends the history.
		e.prev = types.MessageVersion{Content: content, Timestamp: at}
	}
	m.Edits = append(m.Edits, e.prev)
	sort.SliceStable(m.Edits, func(a, b int) bool { return m.Edits[a].Timestamp.Before(m.Edits[b].Timestamp) })
	return e, true
}

// MarkDeleted marks an existing message as deleted (e.g. "delete for me" on
// another device). Unlike RevokeMessage it never creates a placeholder. It
// reports whether anything changed.
func (s *AppState) MarkDeleted(chatKey, msgID string, at time.Time) bool {
	s.messagesMu.Lock()
	msgs := s.messages[chatKey]
	i := indexOf(msgs, msgID)
	if i < 0 || msgs[i].Deleted {
		s.messagesMu.Unlock()
		return false
	}
	msgs[i].Deleted = true
	msgs[i].DeletedAt = at
	stored := msgs[i]
	s.messagesMu.Unlock()

	s.DB.PersistMessage(chatKey, stored)
	return true
}

// RevokeMessage marks a message as deleted. The message stays in the history.
// If the original was never received, a placeholder is created so the
// deletion is still visible. It reports whether anything changed.
func (s *AppState) RevokeMessage(chatKey string, placeholder types.Message, at time.Time) bool {
	s.messagesMu.Lock()
	msgs := s.messages[chatKey]
	var stored types.Message
	if i := indexOf(msgs, placeholder.ID); i >= 0 {
		if msgs[i].Deleted {
			s.messagesMu.Unlock()
			return false
		}
		msgs[i].Deleted = true
		msgs[i].DeletedAt = at
		stored = msgs[i]
	} else {
		placeholder.Content = ""
		placeholder.Deleted = true
		placeholder.DeletedAt = at
		stored, _ = s.upsertLocked(chatKey, placeholder)
	}
	s.messagesMu.Unlock()

	s.DB.PersistMessage(chatKey, stored)
	return true
}

// SetImagePath attaches a downloaded image to a message.
func (s *AppState) SetImagePath(chatKey, msgID, path string) {
	s.messagesMu.Lock()
	if i := indexOf(s.messages[chatKey], msgID); i >= 0 {
		s.messages[chatKey][i].ImagePath = path
	}
	s.messagesMu.Unlock()
	s.DB.SetImagePath(chatKey, msgID, path)
}

func indexOf(msgs []types.Message, id string) int {
	for i := len(msgs) - 1; i >= 0; i-- { // recent messages are the common case
		if msgs[i].ID == id {
			return i
		}
	}
	return -1
}

func hasVersion(versions []types.MessageVersion, content string, at time.Time) bool {
	for _, v := range versions {
		if v.Content == content && v.Timestamp.Equal(at) {
			return true
		}
	}
	return false
}

func sortMessages(msgs []types.Message) {
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].Timestamp.Before(msgs[j].Timestamp) })
}

// ── Chats ─────────────────────────────────────────────────────────────────────

// ChatUpdate describes a change to a chat entry; zero fields are ignored.
type ChatUpdate struct {
	JID      watypes.JID
	Name     string // replaces the name if the current one is not a real name
	LastMsg  string
	LastTime time.Time // last message is updated only if this is newer
	Unread   int       // added to the unread counter
}

// UpdateChat creates or updates a chat entry and persists it.
func (s *AppState) UpdateChat(u ChatUpdate) {
	key := u.JID.String()
	isGroup := u.JID.Server == watypes.GroupServer

	s.chatsMu.Lock()
	c, ok := s.chats[key]
	if !ok {
		c = &types.ChatItem{JID: u.JID, Name: u.JID.User, IsGroup: isGroup}
		s.chats[key] = c
	}
	if u.Name != "" && key != s.selfKey && !HasRealName(*c) {
		c.Name = u.Name
	}
	if !u.LastTime.IsZero() && !u.LastTime.Before(c.LastTime) {
		if u.LastMsg != "" {
			c.LastMsg = u.LastMsg
		}
		c.LastTime = u.LastTime
	}
	c.Unread += u.Unread
	snapshot := *c
	s.chatsMu.Unlock()

	s.DB.UpsertChat(key, snapshot.Name, snapshot.IsGroup, snapshot.LastMsg, snapshot.LastTime)
}

// SetChatName overrides a chat's display name (e.g. with a resolved contact name).
func (s *AppState) SetChatName(jid watypes.JID, name string) {
	if name == "" {
		return
	}
	key := jid.String()
	s.chatsMu.Lock()
	c, ok := s.chats[key]
	ok = ok && key != s.selfKey // the self chat keeps its fixed name
	var isGroup bool
	if ok {
		c.Name = name
		isGroup = c.IsGroup
	}
	s.chatsMu.Unlock()
	if ok {
		s.DB.UpsertChat(key, name, isGroup, "", time.Time{})
	}
}

// SetSelfChat makes sure the chat with the user's own number exists under the
// given name, so the user can send messages to themselves. Its name is not
// changed by contact or push-name updates afterwards.
func (s *AppState) SetSelfChat(jid watypes.JID, name string) {
	key := jid.String()
	s.chatsMu.Lock()
	s.selfKey = key
	c, ok := s.chats[key]
	if !ok {
		c = &types.ChatItem{JID: jid}
		s.chats[key] = c
	}
	c.Name = name
	snapshot := *c
	s.chatsMu.Unlock()
	s.DB.UpsertChat(key, snapshot.Name, false, snapshot.LastMsg, snapshot.LastTime)
}

// IsSelfChat reports whether key is the chat with the user's own number.
func (s *AppState) IsSelfChat(key string) bool {
	s.chatsMu.RLock()
	defer s.chatsMu.RUnlock()
	return key != "" && key == s.selfKey
}

// MergeChat adds a chat entry loaded from another source (DB, contacts,
// groups), keeping fresher data that is already present.
func (s *AppState) MergeChat(c types.ChatItem) {
	key := c.JID.String()
	s.chatsMu.Lock()
	defer s.chatsMu.Unlock()
	existing, ok := s.chats[key]
	if !ok {
		cp := c
		if cp.Name == "" {
			cp.Name = c.JID.User
		}
		s.chats[key] = &cp
		return
	}
	if HasRealName(c) && key != s.selfKey {
		existing.Name = c.Name
	}
	if c.LastTime.After(existing.LastTime) {
		existing.LastMsg = c.LastMsg
		existing.LastTime = c.LastTime
	}
	if !existing.Pinned() {
		existing.PinnedAt = c.PinnedAt
	}
}

// unknownPinTime is used for chats known to be pinned without a pin time.
// They sort after pins with a known time.
var unknownPinTime = time.Unix(1, 0)

// SetPinned pins or unpins a chat, creating the entry if needed. A zero `at`
// keeps the existing pin time. It reports whether anything changed.
func (s *AppState) SetPinned(jid watypes.JID, pinned bool, at time.Time) bool {
	key := jid.String()
	s.chatsMu.Lock()
	c, ok := s.chats[key]
	if !ok {
		if !pinned {
			s.chatsMu.Unlock()
			return false
		}
		c = &types.ChatItem{JID: jid, Name: jid.User, IsGroup: jid.Server == watypes.GroupServer}
		s.chats[key] = c
	}
	old := c.PinnedAt
	switch {
	case !pinned:
		c.PinnedAt = time.Time{}
	case !at.IsZero() && (c.PinnedAt.IsZero() || at.After(c.PinnedAt)):
		c.PinnedAt = at // (re-)pinned later: moves to the top of the pins
	case c.PinnedAt.IsZero():
		c.PinnedAt = unknownPinTime
	}
	changed := !c.PinnedAt.Equal(old)
	pinnedAt := c.PinnedAt
	s.chatsMu.Unlock()

	if changed {
		s.DB.SetPinned(key, pinnedAt)
	}
	return changed
}

// MergeChatInto merges chat `from` (messages and chat entry) into chat `to`
// and removes `from`, in memory and in the database. WhatsApp may address the
// same person by phone number or by LID; this keeps one chat per person.
func (s *AppState) MergeChatInto(from, to watypes.JID) {
	fromKey, toKey := from.String(), to.String()
	if fromKey == toKey {
		return
	}
	// Memory mirrors the database (all of it is loaded at startup), so there
	// is nothing to do if the chat is unknown in memory.
	s.messagesMu.RLock()
	_, hasMsgs := s.messages[fromKey]
	s.messagesMu.RUnlock()
	if _, hasChat := s.Chat(fromKey); !hasMsgs && !hasChat {
		return
	}

	s.messagesMu.Lock()
	moved := len(s.messages[fromKey])
	for _, m := range s.messages[fromKey] {
		s.upsertLocked(toKey, m)
	}
	delete(s.messages, fromKey)
	merged := append([]types.Message(nil), s.messages[toKey]...)
	s.messagesMu.Unlock()

	s.chatsMu.Lock()
	if src, ok := s.chats[fromKey]; ok {
		delete(s.chats, fromKey)
		dst, ok := s.chats[toKey]
		if !ok {
			dst = &types.ChatItem{JID: to, Name: to.User, IsGroup: src.IsGroup}
			s.chats[toKey] = dst
		}
		if toKey != s.selfKey && !HasRealName(*dst) && HasRealName(*src) {
			dst.Name = src.Name
		}
		if src.LastTime.After(dst.LastTime) {
			dst.LastMsg, dst.LastTime = src.LastMsg, src.LastTime
		}
		dst.Unread += src.Unread
		if !dst.Pinned() {
			dst.PinnedAt = src.PinnedAt
		}
	}
	var snapshot types.ChatItem
	if dst, ok := s.chats[toKey]; ok {
		snapshot = *dst
	}
	s.chatsMu.Unlock()

	if moved == 0 {
		merged = nil // nothing to rewrite, only remove the old chat row
	}
	s.DB.MoveChat(fromKey, toKey, merged)
	if !snapshot.JID.IsEmpty() {
		s.DB.UpsertChat(toKey, snapshot.Name, snapshot.IsGroup, snapshot.LastMsg, snapshot.LastTime)
		s.DB.SetPinned(toKey, snapshot.PinnedAt)
	}
}

// HasRealName reports whether a chat has a human-readable name rather than
// a number or its raw JID.
func HasRealName(c types.ChatItem) bool {
	return c.Name != "" && c.Name != c.JID.User && !IsNumeric(c.Name)
}

// Chat returns a copy of a chat entry.
func (s *AppState) Chat(key string) (types.ChatItem, bool) {
	s.chatsMu.RLock()
	defer s.chatsMu.RUnlock()
	if c, ok := s.chats[key]; ok {
		return *c, true
	}
	return types.ChatItem{}, false
}

// ChatList returns all chats, most recent first, then by name.
func (s *AppState) ChatList() []types.ChatItem {
	s.chatsMu.RLock()
	list := make([]types.ChatItem, 0, len(s.chats))
	for _, c := range s.chats {
		list = append(list, *c)
	}
	s.chatsMu.RUnlock()

	sort.Slice(list, func(i, j int) bool {
		// Pinned chats first, most recently pinned on top.
		if list[i].Pinned() != list[j].Pinned() {
			return list[i].Pinned()
		}
		if !list[i].PinnedAt.Equal(list[j].PinnedAt) {
			return list[i].PinnedAt.After(list[j].PinnedAt)
		}
		if !list[i].LastTime.Equal(list[j].LastTime) {
			return list[i].LastTime.After(list[j].LastTime)
		}
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return list[i].JID.String() < list[j].JID.String()
	})
	return list
}

// MarkRead resets the unread counter of a chat. It reports whether it changed.
func (s *AppState) MarkRead(key string) bool {
	s.chatsMu.Lock()
	defer s.chatsMu.Unlock()
	if c, ok := s.chats[key]; ok && c.Unread > 0 {
		c.Unread = 0
		return true
	}
	return false
}

// IsNumeric reports whether s is empty or consists only of digits, i.e. it is
// a phone number rather than a real name.
func IsNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
