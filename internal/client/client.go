// Package client connects whatsmeow events to the application state.
package client

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"DevStarByte/internal/state"
	apptypes "DevStarByte/internal/types"
)

// NewEventHandler returns a whatsmeow event handler wired to the given state.
func NewEventHandler(s *state.AppState) func(interface{}) {
	return func(rawEvt interface{}) {
		switch evt := rawEvt.(type) {
		case *events.Message:
			s.Logger.Debug("Received message event from " + evt.Info.Chat.String())
			handleLiveMessage(s, evt)
			s.Notify()
		case *events.HistorySync:
			s.Logger.Info(fmt.Sprintf("Received history sync with %d conversations", len(evt.Data.GetConversations())))
			handleHistorySync(s, evt)
			s.NotifyHistory()
		case *events.DeleteForMe:
			// "Delete for me" on another device: keep the message, show it as deleted.
			key := canonicalJID(s, evt.ChatJID).String()
			if s.MarkDeleted(key, evt.MessageID, evt.Timestamp) {
				s.Logger.Info("Message " + evt.MessageID + " in " + key + " was deleted for me")
				s.Notify()
			}
		case *events.Pin:
			jid := canonicalJID(s, evt.JID)
			if s.SetPinned(jid, evt.Action.GetPinned(), evt.Timestamp) {
				s.Logger.Info(fmt.Sprintf("Chat %s pinned: %v", jid, evt.Action.GetPinned()))
				s.Notify()
			}
		case *events.Connected:
			s.Logger.Info("Connected to WhatsApp")
			s.SetConnected(true)
		case *events.Disconnected:
			s.Logger.Warning("Disconnected from WhatsApp")
			s.SetConnected(false)
		case *events.LoggedOut:
			s.Logger.Warning("Logged out from WhatsApp")
			s.SetConnected(false)
		}
	}
}

// ── Live messages ─────────────────────────────────────────────────────────────

func handleLiveMessage(s *state.AppState, evt *events.Message) {
	evt = withCanonicalChat(s, evt)
	chatJID := evt.Info.Chat
	if applyProtocolMessage(s, evt) {
		return
	}
	msg, isNew := storeMessage(s, evt)
	if msg == nil {
		s.Logger.Debug("Skipping message without displayable content from " + chatJID.String())
		return
	}
	s.Logger.Info("New message in " + chatJID.String() + " from " + msg.Sender + ": " + truncateLog(msg.Content, 80))

	update := state.ChatUpdate{JID: chatJID, LastMsg: preview(*msg), LastTime: msg.Timestamp}
	if isNew && !msg.FromMe {
		update.Unread = 1
	}
	if _, known := s.Chat(chatJID.String()); !known {
		update.Name = initialChatName(s, chatJID, evt)
	}
	// In DMs the push name of the other side is a better name than a number.
	if chatJID.Server != types.GroupServer && !msg.FromMe && evt.Info.PushName != "" && !state.IsNumeric(evt.Info.PushName) {
		update.Name = evt.Info.PushName
	}
	s.UpdateChat(update)
}

// initialChatName picks a name for a chat seen for the first time.
func initialChatName(s *state.AppState, chatJID types.JID, evt *events.Message) string {
	if chatJID.Server == types.GroupServer {
		// The push name belongs to the sender, not the group: look the group
		// up in the background so the event handler is not blocked.
		go func() {
			info, err := s.Client.GetGroupInfo(context.Background(), chatJID)
			if err != nil {
				s.Logger.Warning("Failed to fetch group info for " + chatJID.String() + ": " + err.Error())
				return
			}
			if info.Name != "" {
				s.SetChatName(chatJID, info.Name)
				s.Notify()
			}
		}()
		return ""
	}
	if name := resolveContactName(s, context.Background(), chatJID); name != "" {
		return name
	}
	if !evt.Info.IsFromMe {
		return evt.Info.PushName
	}
	return ""
}

// applyProtocolMessage handles edits and revokes. It reports whether evt was
// a protocol message (which never shows up as a message of its own).
func applyProtocolMessage(s *state.AppState, evt *events.Message) bool {
	pm := unwrapMessage(evt.Message).GetProtocolMessage()
	if pm == nil {
		return false
	}
	key := evt.Info.Chat.String()
	target := pm.GetKey().GetID()
	switch pm.GetType() {
	case waE2E.ProtocolMessage_REVOKE:
		s.Logger.Info("Message " + target + " in " + key + " was deleted")
		s.RevokeMessage(key, revokePlaceholder(s, evt, target), evt.Info.Timestamp)
	case waE2E.ProtocolMessage_MESSAGE_EDIT:
		content := extractMsgContent(pm.GetEditedMessage())
		s.Logger.Info("Message " + target + " in " + key + " was edited: " + truncateLog(content, 80))
		s.EditMessage(key, target, content, evt.Info.Timestamp)
	}
	return true
}

// revokePlaceholder builds the stand-in shown when a message we never received
// is deleted. The original sender is taken from the revoke key, since group
// admins can delete other people's messages.
func revokePlaceholder(s *state.AppState, evt *events.Message, target string) apptypes.Message {
	key := unwrapMessage(evt.Message).GetProtocolMessage().GetKey()
	m := apptypes.Message{
		ID:        target,
		Sender:    senderName(evt),
		SenderJID: evt.Info.Sender,
		FromMe:    key.GetFromMe(),
		Timestamp: evt.Info.Timestamp,
	}
	if p := key.GetParticipant(); p != "" {
		if jid, err := types.ParseJID(p); err == nil && jid.User != evt.Info.Sender.User {
			m.SenderJID = jid
			m.Sender = jid.User
		}
	}
	if m.FromMe {
		m.Sender = "You"
		if s.Client.Store.ID != nil {
			m.SenderJID = s.Client.Store.ID.ToNonAD()
		}
	}
	return m
}

// storeMessage stores a regular message (and queues its image download). It
// returns nil if the message has nothing to display.
func storeMessage(s *state.AppState, evt *events.Message) (*apptypes.Message, bool) {
	msg := extractMessage(evt)
	if msg == nil {
		return nil, false
	}
	key := evt.Info.Chat.String()
	stored, isNew := s.AddMessage(key, *msg)
	if stored.ImagePath == "" {
		if img := getImageMessage(evt.Message); img != nil {
			queueImageDownload(s, key, msg.ID, img)
		}
	}
	return &stored, isNew
}

// ── History sync ──────────────────────────────────────────────────────────────

func handleHistorySync(s *state.AppState, evt *events.HistorySync) {
	// Push names delivered with this batch, used as fallback when the contact
	// store is not yet populated.
	pushNames := make(map[string]string, len(evt.Data.GetPushnames()))
	for _, pn := range evt.Data.GetPushnames() {
		id, name := pn.GetID(), pn.GetPushname()
		if id == "" || state.IsNumeric(name) {
			continue
		}
		if idx := strings.IndexByte(id, '@'); idx > 0 {
			id = id[:idx]
		}
		pushNames[id] = name
	}

	for _, conv := range evt.Data.GetConversations() {
		processHistoryConversation(s, conv, pushNames)
	}

	// The batch may have taught whatsmeow new LID → phone number mappings.
	mergeLIDChats(s)

	// Chats without a real name may be resolvable now that the contact store
	// and message history have more data.
	for _, c := range s.ChatList() {
		if c.IsGroup || state.HasRealName(c) {
			continue
		}
		name := resolveContactName(s, context.Background(), c.JID)
		if name == "" {
			name = pushNames[c.JID.User]
		}
		s.SetChatName(c.JID, name)
	}
}

func processHistoryConversation(s *state.AppState, conv *waHistorySync.Conversation, pushNames map[string]string) {
	origJID, err := types.ParseJID(conv.GetID())
	if err != nil {
		s.Logger.Warning("Failed to parse history conversation JID: " + conv.GetID())
		return
	}
	jid := canonicalJID(s, origJID)
	if jid.Server == types.HiddenUserServer {
		if pn, err := types.ParseJID(conv.GetPnJID()); err == nil && !pn.IsEmpty() {
			jid = pn.ToNonAD()
		}
	}
	if jid != origJID {
		s.MergeChatInto(origJID, jid) // chat may exist from an earlier sync
	}
	key := jid.String()
	isGroup := jid.Server == types.GroupServer

	// History is sent newest first; process it oldest first so edits and
	// revokes come after the messages they refer to.
	history := slices.Clone(conv.GetMessages())
	slices.SortStableFunc(history, func(a, b *waHistorySync.HistorySyncMsg) int {
		return cmp.Compare(a.GetMessage().GetMessageTimestamp(), b.GetMessage().GetMessageTimestamp())
	})

	var last *apptypes.Message
	lastIncomingName := ""
	count := 0
	for _, histMsg := range history {
		wmi := histMsg.GetMessage()
		if wmi == nil {
			continue
		}
		msg := handleHistoryMessage(s, jid, wmi)
		if msg == nil {
			continue
		}
		count++
		if last == nil || !msg.Timestamp.Before(last.Timestamp) {
			last = msg
		}
		if !msg.FromMe && !state.IsNumeric(msg.Sender) {
			lastIncomingName = msg.Sender
		}
	}
	s.Logger.Debug(fmt.Sprintf("History: %d messages for %s", count, key))

	// Name priority: saved contact name > conversation name > sender push name
	// from the history > push name map of this batch.
	name := firstNonEmpty(conv.GetName(), conv.GetDisplayName())
	if !isGroup {
		if resolved := resolveContactName(s, context.Background(), jid); resolved != "" {
			name = resolved
		}
		for _, candidate := range []string{lastIncomingName, pushNames[jid.User], pushNames[origJID.User], conv.GetUsername()} {
			if !state.IsNumeric(name) {
				break
			}
			name = candidate
		}
	}

	// WhatsApp's own "last activity" time orders chats like the phone does,
	// also when the last event was not a displayable message.
	update := state.ChatUpdate{JID: jid, Name: name}
	if last != nil {
		update.LastMsg = preview(*last)
		update.LastTime = last.Timestamp
	}
	for _, ts := range []uint64{conv.GetConversationTimestamp(), conv.GetLastMsgTimestamp()} {
		if t := timeFromUnix(ts); ts > 0 && t.After(update.LastTime) {
			update.LastTime = t
		}
	}
	s.UpdateChat(update)
	if pinned := conv.GetPinned(); pinned > 0 {
		s.SetPinned(jid, true, timeFromUnix(uint64(pinned)))
	}
}

// handleHistoryMessage stores one history message, or applies it if it is an
// edit or revoke. It returns the stored message, or nil if nothing new is
// displayed for it.
func handleHistoryMessage(s *state.AppState, chatJID types.JID, wmi *waWeb.WebMessageInfo) *apptypes.Message {
	key := chatJID.String()

	// A message deleted before the sync arrives as a bare stub.
	if wmi.GetMessageStubType() == waWeb.WebMessageInfo_REVOKE {
		placeholder := apptypes.Message{
			ID:        wmi.GetKey().GetID(),
			FromMe:    wmi.GetKey().GetFromMe(),
			Sender:    wmi.GetPushName(),
			Timestamp: timeFromUnix(wmi.GetMessageTimestamp()),
		}
		if p, err := types.ParseJID(wmi.GetKey().GetParticipant()); err == nil {
			placeholder.SenderJID = p
		} else if !placeholder.FromMe {
			placeholder.SenderJID = chatJID
		}
		if placeholder.FromMe {
			placeholder.Sender = "You"
		} else if placeholder.Sender == "" {
			placeholder.Sender = placeholder.SenderJID.User
		}
		s.RevokeMessage(key, placeholder, placeholder.Timestamp)
		return nil
	}
	if wmi.GetMessage() == nil {
		return nil
	}

	evt, err := s.Client.ParseWebMessage(chatJID, wmi)
	if err != nil {
		s.Logger.Debug("Skipping history message: " + err.Error())
		return nil
	}
	// ParseWebMessage resolves edits to their target: the event then carries
	// the target's ID and the new content.
	if evt.Info.ID != wmi.GetKey().GetID() {
		s.EditMessage(key, evt.Info.ID, extractMsgContent(evt.Message), evt.Info.Timestamp)
		return nil
	}
	if applyProtocolMessage(s, evt) {
		return nil
	}
	msg, _ := storeMessage(s, evt)
	return msg
}
