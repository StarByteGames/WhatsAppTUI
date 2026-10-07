package client

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/StarGames2025/Logger"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow/types"

	"DevStarByte/internal/state"
	apptypes "DevStarByte/internal/types"
)

// LoadChats fills the chat list from the database, contacts and joined
// groups. Chats already created by events are kept and only enriched.
func LoadChats(s *state.AppState, ctx context.Context) error {
	s.Logger.Info("Loading chat list...")

	// 1. Persisted chats (have last-message info).
	for _, c := range s.DB.LoadChats() {
		s.MergeChat(c)
	}
	setupSelfChat(s)
	mergeLIDChats(s)

	// 2. Contacts (may have better names).
	contacts, err := s.Client.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		s.Logger.Warning("Failed to load contacts: " + err.Error())
	}
	for jid, info := range contacts {
		name := firstNonEmpty(info.FullName, info.PushName, info.BusinessName)
		jid = canonicalJID(s, jid)
		s.MergeChat(apptypes.ChatItem{JID: jid, Name: name, IsGroup: jid.Server == types.GroupServer})
	}

	// 3. Joined groups.
	groups, groupErr := s.Client.GetJoinedGroups(ctx)
	if groupErr != nil {
		s.Logger.Warning("Failed to load groups: " + groupErr.Error())
	}
	for _, g := range groups {
		s.MergeChat(apptypes.ChatItem{JID: g.JID, Name: g.Name, IsGroup: true})
	}

	// Prefer the name saved in the address book for direct chats, and persist
	// it so stale phone-number entries get corrected for subsequent starts.
	for _, c := range s.ChatList() {
		if c.IsGroup {
			continue
		}
		if name := resolveContactName(s, ctx, c.JID); name != "" && name != c.Name && !state.IsNumeric(name) {
			s.SetChatName(c.JID, name)
		}
	}

	applyStoredPins(s, ctx)

	s.Logger.Info(fmt.Sprintf("Loaded %d chats", len(s.ChatList())))
	if groupErr != nil {
		return groupErr
	}
	return err
}

// applyStoredPins applies the pin state whatsmeow saved from earlier app state
// syncs. A chat may have been stored under its LID, so both IDs are checked.
func applyStoredPins(s *state.AppState, ctx context.Context) {
	if s.Client == nil || s.Client.Store == nil || s.Client.Store.ChatSettings == nil {
		return
	}
	for _, c := range s.ChatList() {
		ids := []types.JID{c.JID}
		if c.JID.Server == types.DefaultUserServer && s.Client.Store.LIDs != nil {
			if lid, err := s.Client.Store.LIDs.GetLIDForPN(ctx, c.JID); err == nil && !lid.IsEmpty() {
				ids = append(ids, lid)
			}
		}
		found, pinned := false, false
		for _, id := range ids {
			settings, err := s.Client.Store.ChatSettings.GetChatSettings(ctx, id)
			if err == nil && settings.Found {
				found = true
				pinned = pinned || settings.Pinned
			}
		}
		if found {
			s.SetPinned(c.JID, pinned, time.Time{})
		}
	}
}

// setupSelfChat adds the "message yourself" chat with the user's own number.
func setupSelfChat(s *state.AppState) {
	if s.Client == nil || s.Client.Store == nil || s.Client.Store.ID == nil {
		return
	}
	name := "You"
	if pn := s.Client.Store.PushName; pn != "" {
		name = pn + " (You)"
	}
	s.SetSelfChat(s.Client.Store.ID.ToNonAD(), name)
}

// resolveContactName tries multiple sources to find a human-readable name for a JID.
func resolveContactName(s *state.AppState, ctx context.Context, jid types.JID) string {
	if s.Client != nil && s.Client.Store != nil && s.Client.Store.Contacts != nil {
		if info, err := s.Client.Store.Contacts.GetContact(ctx, jid); err == nil {
			if name := firstNonEmpty(info.FullName, info.PushName, info.BusinessName); name != "" {
				return name
			}
		}
	}
	// Fall back to the sender name of the most recent incoming message.
	if name := s.DB.ResolveNameFromMessages(jid.String()); !state.IsNumeric(name) {
		return name
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

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
