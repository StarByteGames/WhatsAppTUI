package state

import (
	"sync"

	"github.com/StarGames2025/Logger"
	"go.mau.fi/whatsmeow"

	"DevStarByte/internal/db"
	"DevStarByte/internal/types"
)

// AppState holds all shared runtime state that is accessed by both the
// WhatsApp client event handlers and the TUI.
type AppState struct {
	Client *whatsmeow.Client
	DB     *db.Store
	Logger *Logger.Logger

	ChatsMu  sync.RWMutex
	ChatsMap map[string]*types.ChatItem

	MessagesMu  sync.RWMutex
	MessagesMap map[string][]types.Message

	IncomingCh chan types.MsgEvent
	HistoryCh  chan struct{}

	ExitCodes map[string]int
}

// New creates a new AppState with the given dependencies.
func New(client *whatsmeow.Client, store *db.Store, logger *Logger.Logger) *AppState {
	return &AppState{
		Client:      client,
		DB:          store,
		Logger:      logger,
		ChatsMap:    make(map[string]*types.ChatItem),
		MessagesMap: make(map[string][]types.Message),
		IncomingCh:  make(chan types.MsgEvent, 256),
		HistoryCh:   make(chan struct{}, 8),
		ExitCodes: map[string]int{
			"ERROR":                -1,
			"SUCCESS":              0,
			"SHUTDOWN":             0,
			"DB_INIT_ERROR":        10,
			"DEVICE_STORE_ERROR":   11,
			"LOGGER_ERROR":         12,
			"QR_GENERATE_ERROR":    13,
			"QR_OPEN_ERROR":        14,
			"QR_DECODE_ERROR":      15,
			"QR_RESIZE_ERROR":      16,
			"QR_FILE_CREATE_ERROR": 17,
			"QR_FILE_ENCODE_ERROR": 18,
			"QR_RENDER_ERROR":      19,
			"GROUP_FETCH_ERROR":    20,
			"CONTACT_FETCH_ERROR":  21,
			"DATA_MARSHAL_ERROR":   22,
			"DATA_UNMARSHAL_ERROR": 23,
		},
	}
}
