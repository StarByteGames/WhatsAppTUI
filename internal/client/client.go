package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nfnt/resize"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/StarGames2025/Logger"

	"DevStarByte/internal/state"
	apptypes "DevStarByte/internal/types"
)

// ── Event handling ────────────────────────────────────────────────────────────

// NewEventHandler returns a whatsmeow event handler wired to the given state.
func NewEventHandler(s *state.AppState) func(interface{}) {
	return func(rawEvt interface{}) {
		switch evt := rawEvt.(type) {
		case *events.Message:
			s.Logger.Debug("Received message event from " + evt.Info.Chat.String())
			handleMessage(s, evt)
		case *events.HistorySync:
			s.Logger.Info(fmt.Sprintf("Received history sync with %d conversations", len(evt.Data.GetConversations())))
			handleHistorySync(s, evt)
		}
	}
}

func handleHistorySync(s *state.AppState, evt *events.HistorySync) {
	for _, conv := range evt.Data.GetConversations() {
		processHistoryConversation(s, conv)
	}
	// Signal the TUI to rebuild its chat list from the updated global maps.
	select {
	case s.HistoryCh <- struct{}{}:
	default:
	}
}

func processHistoryConversation(s *state.AppState, conv *waHistorySync.Conversation) {
	jid, err := types.ParseJID(conv.GetID())
	if err != nil {
		s.Logger.Warning("Failed to parse history conversation JID: " + conv.GetID())
		return
	}
	s.Logger.Debug("Processing history for: " + jid.String() + " (" + conv.GetName() + ")")
	key := jid.String()

	var msgs []apptypes.Message
	for _, histMsg := range conv.GetMessages() {
		wmi := histMsg.GetMessage()
		if wmi == nil {
			continue
		}
		msg := extractHistoryMessage(s, wmi, jid)
		if msg == nil {
			continue
		}
		msgs = append(msgs, *msg)
		s.DB.PersistMessage(key, *msg)
	}
	s.Logger.Debug(fmt.Sprintf("History: %d messages for %s", len(msgs), key))

	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].Timestamp.Before(msgs[j].Timestamp)
	})

	s.MessagesMu.Lock()
	// Prepend history before any live messages already received.
	existing := s.MessagesMap[key]
	merged := append(msgs, existing...)
	// Deduplicate by message ID.
	seen := make(map[string]bool, len(merged))
	deduped := make([]apptypes.Message, 0, len(merged))
	for _, m := range merged {
		if !seen[m.ID] {
			seen[m.ID] = true
			deduped = append(deduped, m)
		}
	}
	s.MessagesMap[key] = deduped
	s.MessagesMu.Unlock()

	name := conv.GetName()
	if name == "" {
		name = jid.User
	}

	var lastMsg string
	var lastTime time.Time
	if l := len(msgs); l > 0 {
		lastMsg = msgs[l-1].Content
		lastTime = msgs[l-1].Timestamp
	}

	s.ChatsMu.Lock()
	if existing, ok := s.ChatsMap[key]; ok {
		if existing.LastTime.IsZero() {
			existing.LastMsg = lastMsg
			existing.LastTime = lastTime
		}
		if existing.Name == jid.User && name != jid.User {
			existing.Name = name
		}
	} else {
		s.ChatsMap[key] = &apptypes.ChatItem{
			JID:      jid,
			Name:     name,
			LastMsg:  lastMsg,
			LastTime: lastTime,
			IsGroup:  jid.Server == types.GroupServer,
		}
	}
	s.ChatsMu.Unlock()

	// Always persist the chat record.
	s.DB.UpsertChat(key, name, jid.Server == types.GroupServer, lastMsg, lastTime)
}

func extractHistoryMessage(s *state.AppState, wmi *waWeb.WebMessageInfo, chatJID types.JID) *apptypes.Message {
	key := wmi.GetKey()
	m := wmi.GetMessage()
	if m == nil || key == nil {
		return nil
	}

	content := extractMsgContent(m)
	if content == "" {
		return nil
	}

	var senderJID types.JID
	var senderName string
	if key.GetFromMe() {
		senderName = "You"
		if s.Client.Store.ID != nil {
			senderJID = *s.Client.Store.ID
		}
	} else {
		participant := key.GetParticipant()
		if participant == "" {
			// DM: sender is the remote JID
			senderJID = chatJID
		} else {
			var err error
			senderJID, err = types.ParseJID(participant)
			if err != nil {
				senderJID = chatJID
			}
		}
		senderName = wmi.GetPushName()
		if senderName == "" {
			senderName = senderJID.User
		}
	}

	msg := &apptypes.Message{
		ID:        key.GetID(),
		Sender:    senderName,
		SenderJID: senderJID,
		Content:   content,
		Timestamp: time.Unix(int64(wmi.GetMessageTimestamp()), 0),
		FromMe:    key.GetFromMe(),
	}

	// Try to download and cache image if this is an image message.
	if imgMsg := getImageMessage(m); imgMsg != nil {
		if cached := downloadAndCacheImage(s, imgMsg); cached != "" {
			msg.ImagePath = cached
		}
	}

	return msg
}

func handleMessage(s *state.AppState, evt *events.Message) {
	msg := extractMessage(evt)
	if msg == nil {
		s.Logger.Debug("Skipping unparseable message from " + evt.Info.Chat.String())
		return
	}

	// Try to download image if this is an image message.
	if imgMsg := getImageMessage(evt.Message); imgMsg != nil {
		if cached := downloadAndCacheImage(s, imgMsg); cached != "" {
			msg.ImagePath = cached
		}
	}

	s.Logger.Info("New message in " + evt.Info.Chat.String() + " from " + msg.Sender + ": " + truncateLog(msg.Content, 80))

	chatJID := evt.Info.Chat
	key := chatJID.String()

	s.MessagesMu.Lock()
	s.MessagesMap[key] = append(s.MessagesMap[key], *msg)
	s.MessagesMu.Unlock()

	s.DB.PersistMessage(key, *msg)

	s.ChatsMu.Lock()
	var chatName string
	if chat, ok := s.ChatsMap[key]; ok {
		chat.LastMsg = msg.Content
		chat.LastTime = msg.Timestamp
		if !msg.FromMe {
			chat.Unread++
		}
		chatName = chat.Name
	} else {
		name := evt.Info.PushName
		if name == "" {
			if resolved := resolveContactName(s, context.Background(), chatJID); resolved != "" {
				name = resolved
			} else {
				name = chatJID.User
			}
		}
		s.ChatsMap[key] = &apptypes.ChatItem{
			JID:      chatJID,
			Name:     name,
			LastMsg:  msg.Content,
			LastTime: msg.Timestamp,
			IsGroup:  chatJID.Server == types.GroupServer,
		}
		chatName = name
	}
	s.ChatsMu.Unlock()

	s.DB.UpsertChat(key, chatName, chatJID.Server == types.GroupServer, msg.Content, msg.Timestamp)

	// Non-blocking push to TUI message loop.
	select {
	case s.IncomingCh <- apptypes.MsgEvent{ChatJID: chatJID, Message: *msg}:
	default:
	}
}

// ── Message extraction ────────────────────────────────────────────────────────

func extractMessage(evt *events.Message) *apptypes.Message {
	info := evt.Info
	m := evt.Message
	if m == nil {
		return nil
	}
	content := extractMsgContent(m)
	if content == "" {
		return nil
	}
	sender := info.PushName
	if sender == "" {
		sender = info.Sender.User
	}
	return &apptypes.Message{
		ID:        info.ID,
		Sender:    sender,
		SenderJID: info.Sender,
		Content:   content,
		Timestamp: info.Timestamp,
		FromMe:    info.IsFromMe,
	}
}

// extractMsgContent returns a plain-text representation of any message type.
// It recursively unwraps DeviceSentMessage and FutureProofMessage wrappers.
func extractMsgContent(m *waE2E.Message) string {
	if m == nil {
		return ""
	}

	// Unwrap device-sent (messages sent from another linked device).
	if d := m.GetDeviceSentMessage(); d != nil {
		if inner := d.GetMessage(); inner != nil {
			return extractMsgContent(inner)
		}
	}
	// Unwrap ephemeral (disappearing messages).
	if e := m.GetEphemeralMessage(); e != nil {
		if inner := e.GetMessage(); inner != nil {
			return extractMsgContent(inner)
		}
	}
	// Unwrap view-once.
	if v := m.GetViewOnceMessage(); v != nil {
		if inner := v.GetMessage(); inner != nil {
			return extractMsgContent(inner)
		}
	}
	if v := m.GetViewOnceMessageV2(); v != nil {
		if inner := v.GetMessage(); inner != nil {
			return extractMsgContent(inner)
		}
	}
	// Unwrap edited messages.
	if e := m.GetEditedMessage(); e != nil {
		if inner := e.GetMessage(); inner != nil {
			return extractMsgContent(inner)
		}
	}
	// Unwrap document-with-caption.
	if d := m.GetDocumentWithCaptionMessage(); d != nil {
		if inner := d.GetMessage(); inner != nil {
			return extractMsgContent(inner)
		}
	}

	switch {
	case m.GetConversation() != "":
		return m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText()
	case m.GetImageMessage() != nil:
		if cap := m.GetImageMessage().GetCaption(); cap != "" {
			return "[Image: " + cap + "]"
		}
		return "[Image]"
	case m.GetVideoMessage() != nil:
		if cap := m.GetVideoMessage().GetCaption(); cap != "" {
			return "[Video: " + cap + "]"
		}
		return "[Video]"
	case m.GetAudioMessage() != nil:
		return "[Voice message]"
	case m.GetDocumentMessage() != nil:
		if fn := m.GetDocumentMessage().GetFileName(); fn != "" {
			return "[File: " + fn + "]"
		}
		return "[Document]"
	case m.GetStickerMessage() != nil:
		return "[Sticker]"
	case m.GetContactMessage() != nil:
		return "[Contact: " + m.GetContactMessage().GetDisplayName() + "]"
	case m.GetLocationMessage() != nil:
		return "[Location]"
	case m.GetLiveLocationMessage() != nil:
		return "[Live Location]"
	case m.GetListMessage() != nil:
		return "[List: " + m.GetListMessage().GetTitle() + "]"
	case m.GetPollCreationMessage() != nil:
		return "[Poll: " + m.GetPollCreationMessage().GetName() + "]"
	case m.GetPollCreationMessageV2() != nil:
		return "[Poll: " + m.GetPollCreationMessageV2().GetName() + "]"
	case m.GetPollCreationMessageV3() != nil:
		return "[Poll: " + m.GetPollCreationMessageV3().GetName() + "]"
	case m.GetReactionMessage() != nil:
		return "[Reaction: " + m.GetReactionMessage().GetText() + "]"
	case m.GetProtocolMessage() != nil:
		return ""
	}
	return ""
}

// ── Message sending ───────────────────────────────────────────────────────────

// SendMessage sends a text message to a WhatsApp JID.
func SendMessage(s *state.AppState, jid types.JID, text string) error {
	s.Logger.Info("Sending message to " + jid.String() + ": " + truncateLog(text, 80))
	conv := text
	resp, err := s.Client.SendMessage(context.Background(), jid, &waE2E.Message{
		Conversation: &conv,
	})
	if err != nil {
		s.Logger.Error("Failed to send message to " + jid.String() + ": " + err.Error())
		return err
	}
	s.Logger.Info("Message sent successfully, ID: " + resp.ID)

	var senderJID types.JID
	if s.Client.Store.ID != nil {
		senderJID = *s.Client.Store.ID
	}
	msg := apptypes.Message{
		ID:        resp.ID,
		Sender:    "You",
		SenderJID: senderJID,
		Content:   text,
		Timestamp: resp.Timestamp,
		FromMe:    true,
	}
	key := jid.String()

	s.MessagesMu.Lock()
	s.MessagesMap[key] = append(s.MessagesMap[key], msg)
	s.MessagesMu.Unlock()

	s.DB.PersistMessage(key, msg)
	s.DB.UpsertChat(key, "", jid.Server == types.GroupServer, text, resp.Timestamp)

	// Push to TUI so the message appears in the chat view immediately.
	select {
	case s.IncomingCh <- apptypes.MsgEvent{ChatJID: jid, Message: msg}:
	default:
	}

	return nil
}

// ── Image handling ────────────────────────────────────────────────────────────

// downloadAndCacheImage downloads an image message via whatsmeow and saves it
// to media_cache/. Returns the file path on success, or "" on failure.
func downloadAndCacheImage(s *state.AppState, imgMsg *waE2E.ImageMessage) string {
	if imgMsg == nil || s.Client == nil {
		return ""
	}
	data, err := s.Client.Download(context.Background(), imgMsg)
	if err != nil {
		s.Logger.Warning("Failed to download image: " + err.Error())
		return ""
	}
	// Generate a deterministic filename from the content hash.
	h := sha256.Sum256(data)
	name := hex.EncodeToString(h[:8]) + ".jpg"
	fpath := filepath.Join("media_cache", name)

	// Skip if already cached.
	if _, err := os.Stat(fpath); err == nil {
		return fpath
	}

	// Decode → resize → save as JPEG for consistent Sixel rendering.
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		s.Logger.Warning("Failed to decode image: " + err.Error())
		return ""
	}

	// Resize to max 40 columns wide (~320px at typical 8px cell width).
	const maxW = 320
	if img.Bounds().Dx() > maxW {
		img = resize.Resize(maxW, 0, img, resize.Lanczos3)
	}

	f, err := os.Create(fpath)
	if err != nil {
		s.Logger.Warning("Failed to create cache file: " + err.Error())
		return ""
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
		s.Logger.Warning("Failed to encode JPEG: " + err.Error())
		os.Remove(fpath)
		return ""
	}
	s.Logger.Info("Cached image: " + fpath)
	return fpath
}

// getImageMessage unwraps wrapper layers and returns the ImageMessage if present.
func getImageMessage(m *waE2E.Message) *waE2E.ImageMessage {
	if m == nil {
		return nil
	}
	if d := m.GetDeviceSentMessage(); d != nil {
		if r := getImageMessage(d.GetMessage()); r != nil {
			return r
		}
	}
	if e := m.GetEphemeralMessage(); e != nil {
		if r := getImageMessage(e.GetMessage()); r != nil {
			return r
		}
	}
	if v := m.GetViewOnceMessage(); v != nil {
		if r := getImageMessage(v.GetMessage()); r != nil {
			return r
		}
	}
	if v := m.GetViewOnceMessageV2(); v != nil {
		if r := getImageMessage(v.GetMessage()); r != nil {
			return r
		}
	}
	return m.GetImageMessage()
}

// ── Chat loading ──────────────────────────────────────────────────────────────

// LoadChats merges chats from the database, contacts, and joined groups.
func LoadChats(s *state.AppState, ctx context.Context) ([]apptypes.ChatItem, error) {
	s.Logger.Info("Loading chat list...")
	seen := make(map[string]bool)
	byJID := make(map[string]*apptypes.ChatItem)

	// 1. Seed from persisted DB chats (has last-message info).
	for _, c := range s.DB.LoadChats() {
		cp := c
		byJID[c.JID.String()] = &cp
		seen[c.JID.String()] = true
	}

	// 2. Merge with live contacts (may have better names).
	contacts, err := s.Client.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		s.Logger.Warning("Failed to load contacts: " + err.Error())
	}
	for jid, info := range contacts {
		name := info.FullName
		if name == "" {
			name = info.PushName
		}
		if name == "" {
			name = jid.User
		}
		key := jid.String()
		if existing, ok := byJID[key]; ok {
			if name != jid.User {
				existing.Name = name
			}
		} else {
			c := &apptypes.ChatItem{JID: jid, Name: name, IsGroup: jid.Server == types.GroupServer}
			byJID[key] = c
		}
		seen[key] = true
	}

	// 3. Merge with joined groups.
	groups, _ := s.Client.GetJoinedGroups(ctx)
	for _, g := range groups {
		key := g.JID.String()
		if existing, ok := byJID[key]; ok {
			if g.Name != "" {
				existing.Name = g.Name
			}
		} else {
			c := &apptypes.ChatItem{JID: g.JID, Name: g.Name, IsGroup: true}
			byJID[key] = c
		}
		seen[key] = true
	}

	// Build result slice and populate global map.
	result := make([]apptypes.ChatItem, 0, len(byJID))
	s.ChatsMu.Lock()
	for key, c := range byJID {
		// Resolve names still showing as raw phone numbers.
		if looksLikeNumber(c.Name) && !c.IsGroup {
			if resolved := resolveContactName(s, ctx, c.JID); resolved != "" {
				c.Name = resolved
			}
		}
		result = append(result, *c)
		s.ChatsMap[key] = c
	}
	s.ChatsMu.Unlock()

	// Sort: most recent message first, then alphabetically.
	sort.Slice(result, func(i, j int) bool {
		if !result[i].LastTime.Equal(result[j].LastTime) {
			return result[i].LastTime.After(result[j].LastTime)
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
}

// looksLikeNumber returns true if the string is all digits (i.e. a phone number, not a name).
func looksLikeNumber(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// resolveContactName tries multiple sources to find a human-readable name for a JID.
func resolveContactName(s *state.AppState, ctx context.Context, jid types.JID) string {
	// 1. Contact store (push name, full name, business name).
	if info, err := s.Client.Store.Contacts.GetContact(ctx, jid); err == nil {
		if info.FullName != "" {
			return info.FullName
		}
		if info.PushName != "" {
			return info.PushName
		}
		if info.BusinessName != "" {
			return info.BusinessName
		}
	}
	// 2. Look at the sender_name of the most recent message we have from this chat.
	if name := s.DB.ResolveNameFromMessages(jid.String()); name != "" && !looksLikeNumber(name) {
		return name
	}
	return ""
}

// ── QR code display ───────────────────────────────────────────────────────────

// DisplayQR renders a QR code to the terminal.
func DisplayQR(logger *Logger.Logger, code string) {
	q, err := qrcode.New(code, qrcode.Medium)
	if err != nil {
		logger.Error("QR generate failed: " + err.Error())
		fmt.Println(code)
		return
	}
	fmt.Println(q.ToSmallString(false))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func truncateLog(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}
