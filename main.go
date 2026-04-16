package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nfnt/resize"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/skip2/go-qrcode"

	"github.com/StarGames2025/Logger"
)

// ── Global state ──────────────────────────────────────────────────────────────

var (
	exitCodes = map[string]int{
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
	}

	logger, _ = Logger.NewLogger(Logger.DEBUG, "./.log", false)

	waClient   *whatsmeow.Client
	incomingCh = make(chan msgEvent, 256)
	historyCh  = make(chan struct{}, 8)

	msgDB *sql.DB

	chatsMu  sync.RWMutex
	chatsMap = make(map[string]*ChatItem)

	messagesMu  sync.RWMutex
	messagesMap = make(map[string][]Message)
)

// ── Domain types ──────────────────────────────────────────────────────────────

// ChatItem is a sidebar entry representing one conversation.
type ChatItem struct {
	JID      types.JID
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
	SenderJID types.JID
	Content   string
	Timestamp time.Time
	FromMe    bool
	ImagePath string // path to cached image file (empty if not an image)
}

// msgEvent carries an incoming message event to the TUI goroutine.
type msgEvent struct {
	ChatJID types.JID
	Message Message
}

// ── Message DB ────────────────────────────────────────────────────────────────

func initMessageDB() error {
	logger.Info("Initialising message database...")
	db, err := sql.Open("sqlite3", "file:messages.db?_journal_mode=WAL")
	if err != nil {
		logger.Error("Failed to open messages.db: " + err.Error())
		return err
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS messages (
		id          TEXT    NOT NULL,
		chat_jid    TEXT    NOT NULL,
		sender_jid  TEXT    NOT NULL DEFAULT '',
		sender_name TEXT    NOT NULL DEFAULT '',
		content     TEXT    NOT NULL,
		timestamp   INTEGER NOT NULL,
		from_me     INTEGER NOT NULL DEFAULT 0,
		image_path  TEXT    NOT NULL DEFAULT '',
		PRIMARY KEY (id, chat_jid)
	)`); err != nil {
		db.Close()
		return err
	}
	if _, err = db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_msg_chat_ts ON messages(chat_jid, timestamp ASC)`,
	); err != nil {
		db.Close()
		return err
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS chats (
		jid        TEXT PRIMARY KEY,
		name       TEXT NOT NULL DEFAULT '',
		is_group   INTEGER NOT NULL DEFAULT 0,
		last_msg   TEXT NOT NULL DEFAULT '',
		last_ts    INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		db.Close()
		return err
	}
	msgDB = db
	logger.Info("Message database initialised successfully")

	// Migrate: add image_path column if missing (for existing databases).
	_, _ = db.Exec(`ALTER TABLE messages ADD COLUMN image_path TEXT NOT NULL DEFAULT ''`)

	// Ensure media cache directory exists.
	if err := os.MkdirAll("media_cache", 0o755); err != nil {
		logger.Warning("Failed to create media_cache dir: " + err.Error())
	}

	return nil
}

func upsertChat(jid string, name string, isGroup bool, lastMsg string, lastTs time.Time) {
	if msgDB == nil {
		return
	}
	logger.Debug("Upserting chat: " + jid + " name=" + name)
	ig := 0
	if isGroup {
		ig = 1
	}
	_, err := msgDB.Exec(
		`INSERT INTO chats(jid, name, is_group, last_msg, last_ts) VALUES(?,?,?,?,?)
		 ON CONFLICT(jid) DO UPDATE SET
		   name     = CASE WHEN excluded.name != '' THEN excluded.name ELSE name END,
		   is_group = excluded.is_group,
		   last_msg = CASE WHEN excluded.last_ts >= last_ts THEN excluded.last_msg ELSE last_msg END,
		   last_ts  = CASE WHEN excluded.last_ts >= last_ts THEN excluded.last_ts  ELSE last_ts  END`,
		jid, name, ig, lastMsg, lastTs.Unix(),
	)
	if err != nil {
		logger.Error("Failed to upsert chat: " + err.Error())
	}
}

func loadChatsFromDB() []ChatItem {
	if msgDB == nil {
		return nil
	}
	logger.Debug("Loading chats from database...")
	rows, err := msgDB.Query(
		`SELECT jid, name, is_group, last_msg, last_ts FROM chats ORDER BY last_ts DESC`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []ChatItem
	for rows.Next() {
		var jidStr, name, lastMsg string
		var isGroup int
		var lastTs int64
		if err := rows.Scan(&jidStr, &name, &isGroup, &lastMsg, &lastTs); err != nil {
			continue
		}
		jid, err := types.ParseJID(jidStr)
		if err != nil {
			continue
		}
		items = append(items, ChatItem{
			JID:      jid,
			Name:     name,
			IsGroup:  isGroup != 0,
			LastMsg:  lastMsg,
			LastTime: time.Unix(lastTs, 0),
		})
	}
	logger.Info(fmt.Sprintf("Loaded %d chats from database", len(items)))
	return items
}

func persistMessage(chatJID string, msg Message) {
	if msgDB == nil {
		return
	}
	logger.Debug("Persisting message " + msg.ID + " in chat " + chatJID)
	fromMe := 0
	if msg.FromMe {
		fromMe = 1
	}
	_, err := msgDB.Exec(
		`INSERT INTO messages(id, chat_jid, sender_jid, sender_name, content, timestamp, from_me, image_path)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(id, chat_jid) DO UPDATE SET
		   image_path  = CASE WHEN excluded.image_path != '' THEN excluded.image_path ELSE image_path END,
		   sender_name = CASE WHEN excluded.sender_name != '' THEN excluded.sender_name ELSE sender_name END,
		   content     = CASE WHEN excluded.content     != '' THEN excluded.content     ELSE content     END`,
		msg.ID, chatJID, msg.SenderJID.String(), msg.Sender, msg.Content,
		msg.Timestamp.Unix(), fromMe, msg.ImagePath,
	)
	if err != nil {
		logger.Error("Failed to persist message: " + err.Error())
	}
}

func loadMessagesFromDB(chatJID string, limit int) []Message {
	if msgDB == nil {
		return nil
	}
	logger.Debug("Loading messages from DB for chat: " + chatJID)
	rows, err := msgDB.Query(
		`SELECT id, sender_jid, sender_name, content, timestamp, from_me, image_path
		 FROM messages WHERE chat_jid = ? ORDER BY timestamp ASC LIMIT ?`,
		chatJID, limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var msgs []Message
	for rows.Next() {
		var m Message
		var senderJID string
		var ts int64
		var fromMe int
		if err := rows.Scan(&m.ID, &senderJID, &m.Sender, &m.Content, &ts, &fromMe, &m.ImagePath); err != nil {
			continue
		}
		m.SenderJID, _ = types.ParseJID(senderJID)
		m.Timestamp = time.Unix(ts, 0)
		m.FromMe = fromMe != 0
		msgs = append(msgs, m)
	}
	return msgs
}

// loadAllMessagesFromDB reads every persisted message into messagesMap
// so they're available immediately when the TUI starts.
func loadAllMessagesFromDB() {
	if msgDB == nil {
		return
	}
	logger.Info("Bulk-loading all messages from database...")
	rows, err := msgDB.Query(
		`SELECT id, chat_jid, sender_jid, sender_name, content, timestamp, from_me, image_path
		 FROM messages ORDER BY timestamp ASC`,
	)
	if err != nil {
		logger.Error("Failed to bulk-load messages: " + err.Error())
		return
	}
	defer rows.Close()

	messagesMu.Lock()
	defer messagesMu.Unlock()
	for rows.Next() {
		var m Message
		var chatJID, senderJID string
		var ts int64
		var fromMe int
		if err := rows.Scan(&m.ID, &chatJID, &senderJID, &m.Sender, &m.Content, &ts, &fromMe, &m.ImagePath); err != nil {
			continue
		}
		m.SenderJID, _ = types.ParseJID(senderJID)
		m.Timestamp = time.Unix(ts, 0)
		m.FromMe = fromMe != 0
		// Only add if not already present (from a live event that arrived first).
		found := false
		for _, existing := range messagesMap[chatJID] {
			if existing.ID == m.ID {
				found = true
				break
			}
		}
		if !found {
			messagesMap[chatJID] = append(messagesMap[chatJID], m)
		}
	}
	count := 0
	for _, msgs := range messagesMap {
		count += len(msgs)
	}
	logger.Info(fmt.Sprintf("Bulk-loaded %d messages across %d chats", count, len(messagesMap)))
}

// ── Entry point ───────────────────────────────────────────────────────────────

func main() {
	logger.Info("Starting WhatsApp TUI...")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// Initialise SQLite-backed device store.
	logger.Info("Initialising device store...")
	dbLog := waLog.Stdout("Database", "ERROR", true)
	container, err := sqlstore.New(ctx, "sqlite3", "file:whatsapp.db?_foreign_keys=on", dbLog)
	if err != nil {
		logger.Error("DB init failed: " + err.Error())
		os.Exit(exitCodes["DB_INIT_ERROR"])
	}

	if err = initMessageDB(); err != nil {
		logger.Warning("Message DB init failed: " + err.Error())
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		logger.Error("Device store error: " + err.Error())
		os.Exit(exitCodes["DEVICE_STORE_ERROR"])
	}

	// Create whatsmeow client.
	logger.Info("Creating WhatsApp client...")
	clientLog := waLog.Stdout("Client", "ERROR", true)
	waClient = whatsmeow.NewClient(deviceStore, clientLog)
	waClient.AddEventHandler(eventHandler)

	// Connect – pair via QR code if not yet registered.
	if waClient.Store.ID == nil {
		logger.Info("No existing session, starting QR code pairing...")
		qrCh, _ := waClient.GetQRChannel(ctx)
		if err = waClient.Connect(); err != nil {
			logger.Error("Connect failed: " + err.Error())
			os.Exit(exitCodes["ERROR"])
		}
		fmt.Println("\nScan the QR code below with WhatsApp on your phone:\n")
		for evt := range qrCh {
			switch evt.Event {
			case "code":
				displayQR(evt.Code)
			case "success":
				logger.Info("QR code login successful")
				fmt.Println("\n✓ Logged in successfully!")
			case "timeout", "error":
				logger.Error("QR login failed: " + evt.Event)
				os.Exit(exitCodes["ERROR"])
			}
		}
	} else {
		logger.Info("Existing session found, reconnecting...")
		if err = waClient.Connect(); err != nil {
			logger.Error("Connect failed: " + err.Error())
			os.Exit(exitCodes["ERROR"])
		}
	}

	// Let the connection settle before loading chats.
	logger.Debug("Waiting for connection to settle...")
	time.Sleep(2 * time.Second)

	chats, err := loadChats(ctx)
	if err != nil {
		logger.Warning("Partial chat load: " + err.Error())
	}
	logger.Info(fmt.Sprintf("Loaded %d chats", len(chats)))

	// Pre-load all persisted messages into memory so they're available immediately.
	loadAllMessagesFromDB()

	// Start the bubbletea TUI.
	logger.Info("Starting TUI...")
	model := NewModel(chats)
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())

	go func() {
		select {
		case <-sigCh:
			prog.Quit()
		case <-ctx.Done():
		}
	}()

	if _, err = prog.Run(); err != nil {
		logger.Error("TUI error: " + err.Error())
		os.Exit(exitCodes["ERROR"])
	}

	waClient.Disconnect()
	logger.Info("WhatsApp client disconnected")
	if msgDB != nil {
		msgDB.Close()
		logger.Info("Message database closed")
	}
	logger.Info("WhatsApp TUI shutdown complete")
	os.Exit(exitCodes["SUCCESS"])
}

// ── QR code display ───────────────────────────────────────────────────────────

func displayQR(code string) {
	q, err := qrcode.New(code, qrcode.Medium)
	if err != nil {
		logger.Error("QR generate failed: " + err.Error())
		fmt.Println(code)
		return
	}
	fmt.Println(q.ToSmallString(false))
}

// ── Chat loading ──────────────────────────────────────────────────────────────

func loadChats(ctx context.Context) ([]ChatItem, error) {
	logger.Info("Loading chat list...")
	seen := make(map[string]bool)
	byJID := make(map[string]*ChatItem)

	// 1. Seed from persisted DB chats (has last-message info).
	for _, c := range loadChatsFromDB() {
		cp := c
		byJID[c.JID.String()] = &cp
		seen[c.JID.String()] = true
	}

	// 2. Merge with live contacts (may have better names).
	contacts, err := waClient.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		logger.Warning("Failed to load contacts: " + err.Error())
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
			c := &ChatItem{JID: jid, Name: name, IsGroup: jid.Server == types.GroupServer}
			byJID[key] = c
		}
		seen[key] = true
	}

	// 3. Merge with joined groups.
	groups, _ := waClient.GetJoinedGroups(ctx)
	for _, g := range groups {
		key := g.JID.String()
		if existing, ok := byJID[key]; ok {
			if g.Name != "" {
				existing.Name = g.Name
			}
		} else {
			c := &ChatItem{JID: g.JID, Name: g.Name, IsGroup: true}
			byJID[key] = c
		}
		seen[key] = true
	}

	// Build result slice and populate global map.
	result := make([]ChatItem, 0, len(byJID))
	chatsMu.Lock()
	for key, c := range byJID {
		// Resolve names still showing as raw phone numbers.
		if looksLikeNumber(c.Name) && !c.IsGroup {
			if resolved := resolveContactName(ctx, c.JID); resolved != "" {
				c.Name = resolved
			}
		}
		result = append(result, *c)
		chatsMap[key] = c
	}
	chatsMu.Unlock()

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
func resolveContactName(ctx context.Context, jid types.JID) string {
	// 1. Contact store (push name, full name, business name).
	if info, err := waClient.Store.Contacts.GetContact(ctx, jid); err == nil {
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
	if msgDB != nil {
		var name string
		err := msgDB.QueryRow(
			`SELECT sender_name FROM messages
			 WHERE chat_jid = ? AND sender_name != '' AND from_me = 0
			 ORDER BY timestamp DESC LIMIT 1`,
			jid.String(),
		).Scan(&name)
		if err == nil && name != "" && !looksLikeNumber(name) {
			return name
		}
	}
	return ""
}

// ── WhatsApp event handling ───────────────────────────────────────────────────

func eventHandler(rawEvt interface{}) {
	switch evt := rawEvt.(type) {
	case *events.Message:
		logger.Debug("Received message event from " + evt.Info.Chat.String())
		handleMessage(evt)
	case *events.HistorySync:
		logger.Info(fmt.Sprintf("Received history sync with %d conversations", len(evt.Data.GetConversations())))
		handleHistorySync(evt)
	}
}

func handleHistorySync(evt *events.HistorySync) {
	for _, conv := range evt.Data.GetConversations() {
		processHistoryConversation(conv)
	}
	// Signal the TUI to rebuild its chat list from the updated global maps.
	select {
	case historyCh <- struct{}{}:
	default:
	}
}

func processHistoryConversation(conv *waHistorySync.Conversation) {
	jid, err := types.ParseJID(conv.GetID())
	if err != nil {
		logger.Warning("Failed to parse history conversation JID: " + conv.GetID())
		return
	}
	logger.Debug("Processing history for: " + jid.String() + " (" + conv.GetName() + ")")
	key := jid.String()

	var msgs []Message
	for _, histMsg := range conv.GetMessages() {
		wmi := histMsg.GetMessage()
		if wmi == nil {
			continue
		}
		msg := extractHistoryMessage(wmi, jid)
		if msg == nil {
			continue
		}
		msgs = append(msgs, *msg)
		persistMessage(key, *msg)
	}
	logger.Debug(fmt.Sprintf("History: %d messages for %s", len(msgs), key))

	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].Timestamp.Before(msgs[j].Timestamp)
	})

	messagesMu.Lock()
	// Prepend history before any live messages already received.
	existing := messagesMap[key]
	merged := append(msgs, existing...)
	// Deduplicate by message ID.
	seen := make(map[string]bool, len(merged))
	deduped := make([]Message, 0, len(merged))
	for _, m := range merged {
		if !seen[m.ID] {
			seen[m.ID] = true
			deduped = append(deduped, m)
		}
	}
	messagesMap[key] = deduped
	messagesMu.Unlock()

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

	chatsMu.Lock()
	if existing, ok := chatsMap[key]; ok {
		if existing.LastTime.IsZero() {
			existing.LastMsg = lastMsg
			existing.LastTime = lastTime
		}
		if existing.Name == jid.User && name != jid.User {
			existing.Name = name
		}
	} else {
		chatsMap[key] = &ChatItem{
			JID:     jid,
			Name:    name,
			LastMsg: lastMsg,
			LastTime: lastTime,
			IsGroup: jid.Server == types.GroupServer,
		}
	}
	chatsMu.Unlock()

	// Always persist the chat record (even if it had no parseable messages,
	// so the sidebar shows it on next launch).
	upsertChat(key, name, jid.Server == types.GroupServer, lastMsg, lastTime)
}

func extractHistoryMessage(wmi *waWeb.WebMessageInfo, chatJID types.JID) *Message {
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
		if waClient.Store.ID != nil {
			senderJID = *waClient.Store.ID
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

	return &Message{
		ID:        key.GetID(),
		Sender:    senderName,
		SenderJID: senderJID,
		Content:   content,
		Timestamp: time.Unix(int64(wmi.GetMessageTimestamp()), 0),
		FromMe:    key.GetFromMe(),
	}
}

func handleMessage(evt *events.Message) {
	msg := extractMessage(evt)
	if msg == nil {
		logger.Debug("Skipping unparseable message from " + evt.Info.Chat.String())
		return
	}

	// Try to download image if this is an image message.
	if imgMsg := getImageMessage(evt.Message); imgMsg != nil {
		if cached := downloadAndCacheImage(imgMsg); cached != "" {
			msg.ImagePath = cached
		}
	}

	logger.Info("New message in " + evt.Info.Chat.String() + " from " + msg.Sender + ": " + truncateLog(msg.Content, 80))

	chatJID := evt.Info.Chat
	key := chatJID.String()

	messagesMu.Lock()
	messagesMap[key] = append(messagesMap[key], *msg)
	messagesMu.Unlock()

	persistMessage(key, *msg)

	chatsMu.Lock()
	var chatName string
	if chat, ok := chatsMap[key]; ok {
		chat.LastMsg = msg.Content
		chat.LastTime = msg.Timestamp
		if !msg.FromMe {
			chat.Unread++
		}
		chatName = chat.Name
	} else {
		name := evt.Info.PushName
		if name == "" {
			if resolved := resolveContactName(context.Background(), chatJID); resolved != "" {
				name = resolved
			} else {
				name = chatJID.User
			}
		}
		chatsMap[key] = &ChatItem{
			JID:      chatJID,
			Name:     name,
			LastMsg:  msg.Content,
			LastTime: msg.Timestamp,
			IsGroup:  chatJID.Server == types.GroupServer,
		}
		chatName = name
	}
	chatsMu.Unlock()

	upsertChat(key, chatName, chatJID.Server == types.GroupServer, msg.Content, msg.Timestamp)

	// Non-blocking push to TUI message loop.
	select {
	case incomingCh <- msgEvent{ChatJID: chatJID, Message: *msg}:
	default:
	}
}

// downloadAndCacheImage downloads an image message via whatsmeow and saves it
// to media_cache/. Returns the file path on success, or "" on failure.
func downloadAndCacheImage(imgMsg *waE2E.ImageMessage) string {
	if imgMsg == nil || waClient == nil {
		return ""
	}
	data, err := waClient.Download(context.Background(), imgMsg)
	if err != nil {
		logger.Warning("Failed to download image: " + err.Error())
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
		logger.Warning("Failed to decode image: " + err.Error())
		return ""
	}

	// Resize to max 40 columns wide (~320px at typical 8px cell width).
	const maxW = 320
	if img.Bounds().Dx() > maxW {
		img = resize.Resize(maxW, 0, img, resize.Lanczos3)
	}

	f, err := os.Create(fpath)
	if err != nil {
		logger.Warning("Failed to create cache file: " + err.Error())
		return ""
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
		logger.Warning("Failed to encode JPEG: " + err.Error())
		os.Remove(fpath)
		return ""
	}
	logger.Info("Cached image: " + fpath)
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

func extractMessage(evt *events.Message) *Message {
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
	return &Message{
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
		// Protocol messages (revoke, key refresh, etc.) are system messages.
		return ""
	}
	return ""
}

// ── Message sending ───────────────────────────────────────────────────────────

func sendMessage(jid types.JID, text string) error {
	logger.Info("Sending message to " + jid.String() + ": " + truncateLog(text, 80))
	conv := text
	resp, err := waClient.SendMessage(context.Background(), jid, &waE2E.Message{
		Conversation: &conv,
	})
	if err != nil {
		logger.Error("Failed to send message to " + jid.String() + ": " + err.Error())
		return err
	}
	logger.Info("Message sent successfully, ID: " + resp.ID)

	var senderJID types.JID
	if waClient.Store.ID != nil {
		senderJID = *waClient.Store.ID
	}
	msg := Message{
		ID:        resp.ID,
		Sender:    "You",
		SenderJID: senderJID,
		Content:   text,
		Timestamp: resp.Timestamp,
		FromMe:    true,
	}
	key := jid.String()

	messagesMu.Lock()
	messagesMap[key] = append(messagesMap[key], msg)
	messagesMu.Unlock()

	persistMessage(key, msg)
	upsertChat(key, "", jid.Server == types.GroupServer, text, resp.Timestamp)

	// Push to TUI so the message appears in the chat view immediately.
	select {
	case incomingCh <- msgEvent{ChatJID: jid, Message: msg}:
	default:
	}

	return nil
}

// truncateLog shortens a string for log output.
func truncateLog(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}
