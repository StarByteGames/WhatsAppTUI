package client

import (
	"context"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"DevStarByte/internal/state"
	apptypes "DevStarByte/internal/types"
)

// SendMessage sends a text message to a WhatsApp JID and stores it locally.
func SendMessage(s *state.AppState, jid types.JID, text string) error {
	s.Logger.Info("Sending message to " + jid.String() + ": " + truncateLog(text, 80))
	resp, err := s.Client.SendMessage(context.Background(), jid, &waE2E.Message{
		Conversation: &text,
	})
	if err != nil {
		s.Logger.Error("Failed to send message to " + jid.String() + ": " + err.Error())
		return err
	}
	s.Logger.Info("Message sent successfully, ID: " + resp.ID)

	msg := apptypes.Message{
		ID:        resp.ID,
		Sender:    "You",
		Content:   text,
		Timestamp: resp.Timestamp,
		FromMe:    true,
	}
	if s.Client.Store.ID != nil {
		msg.SenderJID = s.Client.Store.ID.ToNonAD()
	}
	s.AddMessage(jid.String(), msg)
	s.UpdateChat(state.ChatUpdate{JID: jid, LastMsg: text, LastTime: resp.Timestamp})
	s.Notify()
	return nil
}
