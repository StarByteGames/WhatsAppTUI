package db

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/StarGames2025/Logger"
	_ "github.com/mattn/go-sqlite3"
	watypes "go.mau.fi/whatsmeow/types"

	"DevStarByte/internal/types"
)

// Store wraps the SQLite message database.
type Store struct {
	db     *sql.DB
	logger *Logger.Logger
}

// NewStore opens (or creates) the message database and returns a Store.
func NewStore(logger *Logger.Logger) (*Store, error) {
	logger.Info("Initialising message database...")
	database, err := sql.Open("sqlite3", "file:messages.db?_journal_mode=WAL")
	if err != nil {
		logger.Error("Failed to open messages.db: " + err.Error())
		return nil, err
	}
	if _, err = database.Exec(`CREATE TABLE IF NOT EXISTS messages (
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
		database.Close()
		return nil, err
	}
	if _, err = database.Exec(
		`CREATE INDEX IF NOT EXISTS idx_msg_chat_ts ON messages(chat_jid, timestamp ASC)`,
	); err != nil {
		database.Close()
		return nil, err
	}
	if _, err = database.Exec(`CREATE TABLE IF NOT EXISTS chats (
		jid        TEXT PRIMARY KEY,
		name       TEXT NOT NULL DEFAULT '',
		is_group   INTEGER NOT NULL DEFAULT 0,
		last_msg   TEXT NOT NULL DEFAULT '',
		last_ts    INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		database.Close()
		return nil, err
	}
	logger.Info("Message database initialised successfully")

	// Migrate: add image_path column if missing (for existing databases).
	_, _ = database.Exec(`ALTER TABLE messages ADD COLUMN image_path TEXT NOT NULL DEFAULT ''`)

	// Ensure media cache directory exists.
	if err := os.MkdirAll("media_cache", 0o755); err != nil {
		logger.Warning("Failed to create media_cache dir: " + err.Error())
	}

	return &Store{db: database, logger: logger}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() {
	if s == nil || s.db == nil {
		return
	}
	s.db.Close()
}

// UpsertChat inserts or updates a chat record.
func (s *Store) UpsertChat(jid string, name string, isGroup bool, lastMsg string, lastTs time.Time) {
	if s == nil || s.db == nil {
		return
	}
	s.logger.Debug("Upserting chat: " + jid + " name=" + name)
	ig := 0
	if isGroup {
		ig = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO chats(jid, name, is_group, last_msg, last_ts) VALUES(?,?,?,?,?)
		 ON CONFLICT(jid) DO UPDATE SET
		   name     = CASE WHEN excluded.name != '' THEN excluded.name ELSE name END,
		   is_group = excluded.is_group,
		   last_msg = CASE WHEN excluded.last_ts >= last_ts THEN excluded.last_msg ELSE last_msg END,
		   last_ts  = CASE WHEN excluded.last_ts >= last_ts THEN excluded.last_ts  ELSE last_ts  END`,
		jid, name, ig, lastMsg, lastTs.Unix(),
	)
	if err != nil {
		s.logger.Error("Failed to upsert chat: " + err.Error())
	}
}

// LoadChats returns all persisted chats, ordered by last message time.
func (s *Store) LoadChats() []types.ChatItem {
	if s == nil || s.db == nil {
		return nil
	}
	s.logger.Debug("Loading chats from database...")
	rows, err := s.db.Query(
		`SELECT jid, name, is_group, last_msg, last_ts FROM chats ORDER BY last_ts DESC`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []types.ChatItem
	for rows.Next() {
		var jidStr, name, lastMsg string
		var isGroup int
		var lastTs int64
		if err := rows.Scan(&jidStr, &name, &isGroup, &lastMsg, &lastTs); err != nil {
			continue
		}
		jid, err := watypes.ParseJID(jidStr)
		if err != nil {
			continue
		}
		items = append(items, types.ChatItem{
			JID:      jid,
			Name:     name,
			IsGroup:  isGroup != 0,
			LastMsg:  lastMsg,
			LastTime: time.Unix(lastTs, 0),
		})
	}
	s.logger.Info(fmt.Sprintf("Loaded %d chats from database", len(items)))
	return items
}

// PersistMessage saves a message to the database.
func (s *Store) PersistMessage(chatJID string, msg types.Message) {
	if s == nil || s.db == nil {
		return
	}
	s.logger.Debug("Persisting message " + msg.ID + " in chat " + chatJID)
	fromMe := 0
	if msg.FromMe {
		fromMe = 1
	}
	_, err := s.db.Exec(
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
		s.logger.Error("Failed to persist message: " + err.Error())
	}
}

// LoadMessages returns messages for a specific chat, ordered by time.
func (s *Store) LoadMessages(chatJID string, limit int) []types.Message {
	if s == nil || s.db == nil {
		return nil
	}
	s.logger.Debug("Loading messages from DB for chat: " + chatJID)
	rows, err := s.db.Query(
		`SELECT id, sender_jid, sender_name, content, timestamp, from_me, image_path
		 FROM messages WHERE chat_jid = ? ORDER BY timestamp ASC LIMIT ?`,
		chatJID, limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var msgs []types.Message
	for rows.Next() {
		var m types.Message
		var senderJID string
		var ts int64
		var fromMe int
		if err := rows.Scan(&m.ID, &senderJID, &m.Sender, &m.Content, &ts, &fromMe, &m.ImagePath); err != nil {
			continue
		}
		m.SenderJID, _ = watypes.ParseJID(senderJID)
		m.Timestamp = time.Unix(ts, 0)
		m.FromMe = fromMe != 0
		msgs = append(msgs, m)
	}
	return msgs
}

// LoadAllMessages returns every persisted message grouped by chat JID.
func (s *Store) LoadAllMessages() map[string][]types.Message {
	if s == nil || s.db == nil {
		return nil
	}
	s.logger.Info("Bulk-loading all messages from database...")
	rows, err := s.db.Query(
		`SELECT id, chat_jid, sender_jid, sender_name, content, timestamp, from_me, image_path
		 FROM messages ORDER BY timestamp ASC`,
	)
	if err != nil {
		s.logger.Error("Failed to bulk-load messages: " + err.Error())
		return nil
	}
	defer rows.Close()

	result := make(map[string][]types.Message)
	for rows.Next() {
		var m types.Message
		var chatJID, senderJID string
		var ts int64
		var fromMe int
		if err := rows.Scan(&m.ID, &chatJID, &senderJID, &m.Sender, &m.Content, &ts, &fromMe, &m.ImagePath); err != nil {
			continue
		}
		m.SenderJID, _ = watypes.ParseJID(senderJID)
		m.Timestamp = time.Unix(ts, 0)
		m.FromMe = fromMe != 0
		result[chatJID] = append(result[chatJID], m)
	}
	count := 0
	for _, msgs := range result {
		count += len(msgs)
	}
	s.logger.Info(fmt.Sprintf("Bulk-loaded %d messages across %d chats", count, len(result)))
	return result
}

// ResolveNameFromMessages looks at message history to find a name for a JID.
func (s *Store) ResolveNameFromMessages(jid string) string {
	if s == nil || s.db == nil {
		return ""
	}
	var name string
	err := s.db.QueryRow(
		`SELECT sender_name FROM messages
		 WHERE chat_jid = ? AND sender_name != '' AND from_me = 0
		 ORDER BY timestamp DESC LIMIT 1`,
		jid,
	).Scan(&name)
	if err == nil && name != "" {
		return name
	}
	return ""
}
