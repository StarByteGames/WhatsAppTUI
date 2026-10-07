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

// MediaCacheDir is where downloaded images are stored.
const MediaCacheDir = "media_cache"

// Store wraps the SQLite message database. All methods are safe to call on a
// nil *Store, in which case they do nothing (persistence is optional).
type Store struct {
	db     *sql.DB
	logger *Logger.Logger
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS messages (
		id          TEXT    NOT NULL,
		chat_jid    TEXT    NOT NULL,
		sender_jid  TEXT    NOT NULL DEFAULT '',
		sender_name TEXT    NOT NULL DEFAULT '',
		content     TEXT    NOT NULL,
		timestamp   INTEGER NOT NULL,
		from_me     INTEGER NOT NULL DEFAULT 0,
		image_path  TEXT    NOT NULL DEFAULT '',
		PRIMARY KEY (id, chat_jid)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_msg_chat_ts ON messages(chat_jid, timestamp ASC)`,
	`CREATE TABLE IF NOT EXISTS chats (
		jid        TEXT PRIMARY KEY,
		name       TEXT NOT NULL DEFAULT '',
		is_group   INTEGER NOT NULL DEFAULT 0,
		last_msg   TEXT NOT NULL DEFAULT '',
		last_ts    INTEGER NOT NULL DEFAULT 0
	)`,
	// Previous versions of edited messages.
	`CREATE TABLE IF NOT EXISTS message_edits (
		chat_jid  TEXT    NOT NULL,
		msg_id    TEXT    NOT NULL,
		content   TEXT    NOT NULL,
		timestamp INTEGER NOT NULL,
		PRIMARY KEY (chat_jid, msg_id, timestamp)
	)`,
}

// Columns added after the initial release; created on demand for old databases.
var migrations = []struct{ table, column, def string }{
	{"messages", "image_path", "TEXT NOT NULL DEFAULT ''"},
	{"messages", "deleted", "INTEGER NOT NULL DEFAULT 0"},
	{"messages", "deleted_at", "INTEGER NOT NULL DEFAULT 0"},
	{"messages", "edited_at", "INTEGER NOT NULL DEFAULT 0"},
	{"chats", "pinned_at", "INTEGER NOT NULL DEFAULT 0"},
}

// NewStore opens (or creates) the message database and returns a Store.
func NewStore(logger *Logger.Logger) (*Store, error) {
	logger.Info("Initialising message database...")
	database, err := sql.Open("sqlite3", "file:messages.db?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		logger.Error("Failed to open messages.db: " + err.Error())
		return nil, err
	}
	for _, stmt := range schema {
		if _, err = database.Exec(stmt); err != nil {
			database.Close()
			return nil, err
		}
	}
	for _, mig := range migrations {
		if err = addColumnIfMissing(database, mig.table, mig.column, mig.def); err != nil {
			database.Close()
			return nil, fmt.Errorf("migrate %s.%s: %w", mig.table, mig.column, err)
		}
	}
	logger.Info("Message database initialised successfully")

	if err := os.MkdirAll(MediaCacheDir, 0o755); err != nil {
		logger.Warning("Failed to create media_cache dir: " + err.Error())
	}

	return &Store{db: database, logger: logger}, nil
}

func addColumnIfMissing(database *sql.DB, table, column, def string) error {
	rows, err := database.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = database.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, def))
	return err
}

// Close closes the underlying database connection.
func (s *Store) Close() {
	if s == nil || s.db == nil {
		return
	}
	s.db.Close()
}

func (s *Store) exec(what string, query string, args ...any) {
	if _, err := s.db.Exec(query, args...); err != nil {
		s.logger.Error("Failed to " + what + ": " + err.Error())
	}
}

// ── Chats ─────────────────────────────────────────────────────────────────────

// UpsertChat inserts or updates a chat record. An empty name keeps the stored
// name, and the last message is only replaced by a newer one.
func (s *Store) UpsertChat(jid string, name string, isGroup bool, lastMsg string, lastTs time.Time) {
	if s == nil || s.db == nil {
		return
	}
	s.exec("upsert chat",
		`INSERT INTO chats(jid, name, is_group, last_msg, last_ts) VALUES(?,?,?,?,?)
		 ON CONFLICT(jid) DO UPDATE SET
		   name     = CASE WHEN excluded.name != '' THEN excluded.name ELSE name END,
		   is_group = excluded.is_group,
		   last_msg = CASE WHEN excluded.last_ts >= last_ts THEN excluded.last_msg ELSE last_msg END,
		   last_ts  = CASE WHEN excluded.last_ts >= last_ts THEN excluded.last_ts  ELSE last_ts  END`,
		jid, name, boolInt(isGroup), lastMsg, unixOrZero(lastTs),
	)
}

// SetPinned stores when a chat was pinned (zero time = not pinned).
func (s *Store) SetPinned(jid string, pinnedAt time.Time) {
	if s == nil || s.db == nil {
		return
	}
	s.exec("set pinned",
		`INSERT INTO chats(jid, pinned_at) VALUES(?, ?)
		 ON CONFLICT(jid) DO UPDATE SET pinned_at = excluded.pinned_at`,
		jid, unixOrZero(pinnedAt),
	)
}

// LoadChats returns all persisted chats, ordered by last message time.
func (s *Store) LoadChats() []types.ChatItem {
	if s == nil || s.db == nil {
		return nil
	}
	rows, err := s.db.Query(
		`SELECT jid, name, is_group, last_msg, last_ts, pinned_at FROM chats ORDER BY last_ts DESC`,
	)
	if err != nil {
		s.logger.Error("Failed to load chats: " + err.Error())
		return nil
	}
	defer rows.Close()
	var items []types.ChatItem
	for rows.Next() {
		var jidStr, name, lastMsg string
		var isGroup int
		var lastTs, pinnedAt int64
		if err := rows.Scan(&jidStr, &name, &isGroup, &lastMsg, &lastTs, &pinnedAt); err != nil {
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
			LastTime: fromUnix(lastTs),
			PinnedAt: fromUnix(pinnedAt),
		})
	}
	s.logger.Info(fmt.Sprintf("Loaded %d chats from database", len(items)))
	return items
}

// ── Messages ──────────────────────────────────────────────────────────────────

// PersistMessage inserts a message or merges it into an existing row.
//
// Merge rules: a deletion is never undone, the content of an edited message is
// never overwritten by an older copy, and a placeholder row (created for a
// revoke whose original we never had) adopts the real content and timestamp.
func (s *Store) PersistMessage(chatJID string, msg types.Message) {
	if s == nil || s.db == nil {
		return
	}
	s.exec("persist message",
		`INSERT INTO messages(id, chat_jid, sender_jid, sender_name, content, timestamp, from_me, image_path, deleted, deleted_at, edited_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id, chat_jid) DO UPDATE SET
		   timestamp   = CASE WHEN content = '' AND excluded.content != '' THEN excluded.timestamp ELSE timestamp END,
		   content     = CASE WHEN edited_at = 0 AND excluded.content != '' THEN excluded.content ELSE content END,
		   image_path  = CASE WHEN excluded.image_path  != '' THEN excluded.image_path  ELSE image_path  END,
		   sender_name = CASE WHEN excluded.sender_name != '' THEN excluded.sender_name ELSE sender_name END,
		   sender_jid  = CASE WHEN excluded.sender_jid  != '' THEN excluded.sender_jid  ELSE sender_jid  END,
		   deleted     = MAX(deleted, excluded.deleted),
		   deleted_at  = CASE WHEN deleted_at = 0 THEN excluded.deleted_at ELSE deleted_at END`,
		msg.ID, chatJID, jidString(msg.SenderJID), msg.Sender, msg.Content,
		msg.Timestamp.Unix(), boolInt(msg.FromMe), msg.ImagePath,
		boolInt(msg.Deleted), unixOrZero(msg.DeletedAt), unixOrZero(msg.EditedAt),
	)
}

// MoveChat moves a chat's data to another chat JID in one transaction (used
// when a LID chat is merged into the phone-number chat of the same person).
// msgs must be the already merged messages of the target chat: they are
// written as-is, then all rows of the old JID are removed.
func (s *Store) MoveChat(from, to string, msgs []types.Message) {
	if s == nil || s.db == nil {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		s.logger.Error("Failed to begin chat move: " + err.Error())
		return
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	for _, m := range msgs {
		if _, err = tx.Exec(
			`INSERT OR REPLACE INTO messages(id, chat_jid, sender_jid, sender_name, content, timestamp, from_me, image_path, deleted, deleted_at, edited_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			m.ID, to, jidString(m.SenderJID), m.Sender, m.Content,
			m.Timestamp.Unix(), boolInt(m.FromMe), m.ImagePath,
			boolInt(m.Deleted), unixOrZero(m.DeletedAt), unixOrZero(m.EditedAt),
		); err != nil {
			s.logger.Error("Failed to move message: " + err.Error())
			return
		}
		for _, v := range m.Edits {
			if _, err = tx.Exec(
				`INSERT OR IGNORE INTO message_edits(chat_jid, msg_id, content, timestamp) VALUES(?,?,?,?)`,
				to, m.ID, v.Content, v.Timestamp.Unix(),
			); err != nil {
				s.logger.Error("Failed to move message version: " + err.Error())
				return
			}
		}
	}
	for _, q := range []string{
		`DELETE FROM messages WHERE chat_jid = ?`,
		`DELETE FROM message_edits WHERE chat_jid = ?`,
		`DELETE FROM chats WHERE jid = ?`,
	} {
		if _, err = tx.Exec(q, from); err != nil {
			s.logger.Error("Failed to remove moved chat: " + err.Error())
			return
		}
	}
	if err = tx.Commit(); err != nil {
		s.logger.Error("Failed to commit chat move: " + err.Error())
		return
	}
	s.logger.Info(fmt.Sprintf("Moved %d messages from %s to %s", len(msgs), from, to))
}

// SaveEdit records a previous version of a message and, if current is true,
// replaces the message's content with newContent.
func (s *Store) SaveEdit(chatJID, msgID string, prev types.MessageVersion, newContent string, editedAt time.Time, current bool) {
	if s == nil || s.db == nil {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		s.logger.Error("Failed to begin edit transaction: " + err.Error())
		return
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err = tx.Exec(
		`INSERT OR IGNORE INTO message_edits(chat_jid, msg_id, content, timestamp) VALUES(?,?,?,?)`,
		chatJID, msgID, prev.Content, prev.Timestamp.Unix(),
	); err != nil {
		s.logger.Error("Failed to save message version: " + err.Error())
		return
	}
	if current {
		if _, err = tx.Exec(
			`UPDATE messages SET content = ?, edited_at = ? WHERE chat_jid = ? AND id = ?`,
			newContent, editedAt.Unix(), chatJID, msgID,
		); err != nil {
			s.logger.Error("Failed to apply message edit: " + err.Error())
			return
		}
	}
	if err = tx.Commit(); err != nil {
		s.logger.Error("Failed to commit message edit: " + err.Error())
	}
}

// SetImagePath stores the cached image path for a message.
func (s *Store) SetImagePath(chatJID, msgID, path string) {
	if s == nil || s.db == nil {
		return
	}
	s.exec("set image path",
		`UPDATE messages SET image_path = ? WHERE chat_jid = ? AND id = ?`,
		path, chatJID, msgID,
	)
}

// LoadAllMessages returns every persisted message grouped by chat JID,
// including the edit history of each message.
func (s *Store) LoadAllMessages() map[string][]types.Message {
	if s == nil || s.db == nil {
		return nil
	}
	s.logger.Info("Bulk-loading all messages from database...")
	edits := s.loadAllEdits()

	rows, err := s.db.Query(
		`SELECT id, chat_jid, sender_jid, sender_name, content, timestamp, from_me, image_path, deleted, deleted_at, edited_at
		 FROM messages ORDER BY timestamp ASC`,
	)
	if err != nil {
		s.logger.Error("Failed to bulk-load messages: " + err.Error())
		return nil
	}
	defer rows.Close()

	result := make(map[string][]types.Message)
	count := 0
	for rows.Next() {
		var m types.Message
		var chatJID, senderJID string
		var ts, deletedAt, editedAt int64
		var fromMe, deleted int
		if err := rows.Scan(&m.ID, &chatJID, &senderJID, &m.Sender, &m.Content, &ts,
			&fromMe, &m.ImagePath, &deleted, &deletedAt, &editedAt); err != nil {
			continue
		}
		m.SenderJID, _ = watypes.ParseJID(senderJID)
		m.Timestamp = time.Unix(ts, 0)
		m.FromMe = fromMe != 0
		m.Deleted = deleted != 0
		m.DeletedAt = fromUnix(deletedAt)
		m.EditedAt = fromUnix(editedAt)
		m.Edits = edits[editKey{chatJID, m.ID}]
		result[chatJID] = append(result[chatJID], m)
		count++
	}
	s.logger.Info(fmt.Sprintf("Bulk-loaded %d messages across %d chats", count, len(result)))
	return result
}

type editKey struct{ chat, id string }

func (s *Store) loadAllEdits() map[editKey][]types.MessageVersion {
	rows, err := s.db.Query(
		`SELECT chat_jid, msg_id, content, timestamp FROM message_edits ORDER BY timestamp ASC`,
	)
	if err != nil {
		s.logger.Error("Failed to load message edits: " + err.Error())
		return nil
	}
	defer rows.Close()
	result := make(map[editKey][]types.MessageVersion)
	for rows.Next() {
		var k editKey
		var v types.MessageVersion
		var ts int64
		if err := rows.Scan(&k.chat, &k.id, &v.Content, &ts); err != nil {
			continue
		}
		v.Timestamp = time.Unix(ts, 0)
		result[k] = append(result[k], v)
	}
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
	if err != nil {
		return ""
	}
	return name
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// unixOrZero maps the zero time to 0 instead of a large negative number.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(ts int64) time.Time {
	if ts == 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

func jidString(j watypes.JID) string {
	if j.IsEmpty() {
		return ""
	}
	return j.String()
}
