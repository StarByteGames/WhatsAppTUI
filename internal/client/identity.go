package client

import (
	"context"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"DevStarByte/internal/state"
)

// canonicalJID returns the JID a chat is stored under. WhatsApp addresses
// people either by phone number or by an anonymous LID ("…@lid"); contact
// names are stored by phone number, so LIDs are mapped to their phone number
// whenever whatsmeow knows it. This keeps one chat per person.
func canonicalJID(s *state.AppState, jid types.JID) types.JID {
	jid = jid.ToNonAD()
	if jid.Server != types.HiddenUserServer || s.Client == nil || s.Client.Store == nil || s.Client.Store.LIDs == nil {
		return jid
	}
	pn, err := s.Client.Store.LIDs.GetPNForLID(context.Background(), jid)
	if err != nil || pn.IsEmpty() {
		return jid
	}
	return pn.ToNonAD()
}

// withCanonicalChat returns evt with its chat JID canonicalized. The event is
// copied rather than modified, since other handlers receive the same pointer.
func withCanonicalChat(s *state.AppState, evt *events.Message) *events.Message {
	chat := canonicalJID(s, evt.Info.Chat)
	if chat == evt.Info.Chat {
		return evt
	}
	cp := *evt
	cp.Info.Chat = chat
	return &cp
}

// mergeLIDChats merges every LID chat whose phone number has become known into
// the phone-number chat of the same person.
func mergeLIDChats(s *state.AppState) {
	for _, c := range s.ChatList() {
		if c.JID.Server != types.HiddenUserServer {
			continue
		}
		if pn := canonicalJID(s, c.JID); pn != c.JID {
			s.Logger.Info("Merging chat " + c.JID.String() + " into " + pn.String())
			s.MergeChatInto(c.JID, pn)
		}
	}
}
