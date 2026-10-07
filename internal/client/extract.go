package client

import (
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"

	apptypes "DevStarByte/internal/types"
)

// extractMessage converts a message event into an app message. It returns nil
// if the message has no displayable content.
func extractMessage(evt *events.Message) *apptypes.Message {
	content := extractMsgContent(evt.Message)
	if content == "" {
		return nil
	}
	return &apptypes.Message{
		ID:        evt.Info.ID,
		Sender:    senderName(evt),
		SenderJID: evt.Info.Sender,
		Content:   content,
		Timestamp: evt.Info.Timestamp,
		FromMe:    evt.Info.IsFromMe,
	}
}

func senderName(evt *events.Message) string {
	if evt.Info.IsFromMe {
		return "You"
	}
	if evt.Info.PushName != "" {
		return evt.Info.PushName
	}
	return evt.Info.Sender.User
}

// unwrapMessage strips wrapper layers (device-sent, ephemeral, view-once, …).
// whatsmeow already unwraps most of them, but nested combinations exist.
func unwrapMessage(m *waE2E.Message) *waE2E.Message {
	for m != nil {
		var inner *waE2E.Message
		switch {
		case m.GetDeviceSentMessage().GetMessage() != nil:
			inner = m.GetDeviceSentMessage().GetMessage()
		case m.GetEphemeralMessage().GetMessage() != nil:
			inner = m.GetEphemeralMessage().GetMessage()
		case m.GetViewOnceMessage().GetMessage() != nil:
			inner = m.GetViewOnceMessage().GetMessage()
		case m.GetViewOnceMessageV2().GetMessage() != nil:
			inner = m.GetViewOnceMessageV2().GetMessage()
		case m.GetViewOnceMessageV2Extension().GetMessage() != nil:
			inner = m.GetViewOnceMessageV2Extension().GetMessage()
		case m.GetEditedMessage().GetMessage() != nil:
			inner = m.GetEditedMessage().GetMessage()
		case m.GetDocumentWithCaptionMessage().GetMessage() != nil:
			inner = m.GetDocumentWithCaptionMessage().GetMessage()
		default:
			return m
		}
		m = inner
	}
	return nil
}

// extractMsgContent returns a plain-text representation of any message type,
// or "" if the message should not be displayed.
func extractMsgContent(m *waE2E.Message) string {
	m = unwrapMessage(m)
	if m == nil {
		return ""
	}
	switch {
	case m.GetConversation() != "":
		return m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText()
	case m.GetImageMessage() != nil:
		return withCaption("[Image", m.GetImageMessage().GetCaption())
	case m.GetVideoMessage() != nil:
		return withCaption("[Video", m.GetVideoMessage().GetCaption())
	case m.GetAudioMessage() != nil:
		return "[Voice message]"
	case m.GetDocumentMessage() != nil:
		if fn := m.GetDocumentMessage().GetFileName(); fn != "" {
			return "[File: " + fn + "]"
		}
		return withCaption("[Document", m.GetDocumentMessage().GetCaption())
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
		// An empty reaction text means a reaction was removed.
		if text := m.GetReactionMessage().GetText(); text != "" {
			return "[Reaction: " + text + "]"
		}
	}
	return ""
}

func withCaption(label, caption string) string {
	if caption != "" {
		return label + ": " + caption + "]"
	}
	return label + "]"
}

// getImageMessage returns the ImageMessage inside m, if any.
func getImageMessage(m *waE2E.Message) *waE2E.ImageMessage {
	return unwrapMessage(m).GetImageMessage()
}

// preview is the chat-list summary of a message.
func preview(m apptypes.Message) string {
	if m.Deleted {
		return "[deleted]"
	}
	return m.Content
}

func timeFromUnix(ts uint64) time.Time {
	return time.Unix(int64(ts), 0)
}

func truncateLog(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}
