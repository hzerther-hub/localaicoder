// Package sessions 多会话管理：保存 / 加载 / 切换 / 删除对话
// （对译 Python sessions.py；SQLite schema 与之完全兼容，旧 JSON 自动迁移）。
package sessions

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（保持 CLI 静态构建）

	"localai/internal/config"
	"localai/internal/msg"
)

var (
	mu          sync.Mutex
	db          *sql.DB
	dbPath      string
	migratedFor string
)

func getDB() (*sql.DB, error) {
	mu.Lock()
	defer mu.Unlock()
	path := config.SessionsDB()
	if db != nil && dbPath == path {
		if err := db.Ping(); err == nil {
			return db, nil
		}
		_ = db.Close()
		db = nil
	}
	if db != nil && dbPath != path {
		_ = db.Close() // 配置目录切换（测试/多实例）：关闭旧连接
		db = nil
	}
	_ = os.MkdirAll(config.Dir(), 0o755)
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	if _, err := d.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = d.Close()
		return nil, err
	}
	if _, err := d.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		_ = d.Close()
		return nil, err
	}
	if _, err := d.Exec(
		"CREATE TABLE IF NOT EXISTS sessions (" +
			" id TEXT PRIMARY KEY," +
			" title TEXT NOT NULL," +
			" created REAL NOT NULL," +
			" updated REAL NOT NULL," +
			" workspace TEXT DEFAULT ''," +
			" messages TEXT NOT NULL)"); err != nil {
		_ = d.Close()
		return nil, err
	}
	if _, err := d.Exec(
		"CREATE INDEX IF NOT EXISTS idx_sessions_workspace_updated" +
			" ON sessions(workspace, updated)"); err != nil {
		_ = d.Close()
		return nil, err
	}
	if _, err := d.Exec(
		"CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated)"); err != nil {
		_ = d.Close()
		return nil, err
	}
	db, dbPath = d, path
	return d, nil
}

// migrateLegacy 把旧的 sessions/*.json 一次性导入 SQLite（已存在记录不覆盖）。
func migrateLegacy(d *sql.DB) {
	legacy := config.LegacySessionsDir()
	if migratedFor == legacy {
		return
	}
	entries, err := os.ReadDir(legacy)
	if err != nil {
		migratedFor = legacy
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		sid := strings.TrimSuffix(name, ".json")
		raw, err := os.ReadFile(filepath.Join(legacy, name))
		if err != nil {
			continue
		}
		var d2 map[string]any
		if json.Unmarshal(raw, &d2) != nil {
			continue
		}
		messages := msg.L(d2, "messages")
		if messages == nil {
			messages = []any{}
		}
		notes := msg.L(d2, "notes")
		title := msg.S(d2, "title")
		if title == "" {
			title = "（无标题）"
		}
		created, updated := time.Now().Unix(), time.Now().Unix()
		if c := msg.F(d2, "created"); c > 0 {
			created = int64(c)
		}
		if u := msg.F(d2, "updated"); u > 0 {
			updated = int64(u)
		}
		_ = upsert(d, sid, title, messages, &created, &updated,
			msg.S(d2, "workspace"), false, notes)
	}
	migratedFor = legacy
}

func upsert(d *sql.DB, sid, title string, messages []any,
	created, updated *int64, workspace string, overwriteIfNewer bool, notes []any) error {
	now := time.Now().Unix()
	if created == nil {
		created = &now
	}
	if updated == nil {
		updated = &now
	}
	payload, _ := json.Marshal(map[string]any{"messages": messages, "notes": notes})

	// created/updated 为 REAL（float64），必须扫进 float64 再转整型（见下方 Load 注释）。
	var oldCreatedF, oldUpdatedF float64
	var oldWS string
	err := d.QueryRow(
		"SELECT created, updated, workspace FROM sessions WHERE id=?", sid).
		Scan(&oldCreatedF, &oldUpdatedF, &oldWS)
	if err == sql.ErrNoRows {
		_, err := d.Exec(
			"INSERT INTO sessions (id, title, created, updated, workspace, messages)"+
				" VALUES (?,?,?,?,?,?)",
			sid, title, *created, *updated, workspace, string(payload))
		return err
	}
	if err != nil {
		return err
	}
	oldUpdated := int64(oldUpdatedF)
	_ = int64(oldCreatedF)
	if overwriteIfNewer || *updated > oldUpdated {
		if workspace == "" {
			workspace = oldWS // 未传 workspace 时保留原值（目录过滤不丢）
		}
		newUpdated := *updated
		if newUpdated < oldUpdated {
			newUpdated = oldUpdated // updated 单调不减
		}
		_, err := d.Exec(
			"UPDATE sessions SET title=?, updated=?, workspace=?, messages=? WHERE id=?",
			title, newUpdated, workspace, string(payload), sid)
		return err
	}
	return nil
}

// Session 一条会话的完整数据。
type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Created   int64  `json:"created"`
	Updated   int64  `json:"updated"`
	Workspace string `json:"workspace"`
	Messages  []any  `json:"messages"`
	Notes     []any  `json:"notes"`
}

// Meta 会话列表项。
type Meta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Updated   int64  `json:"updated"`
	Workspace string `json:"workspace"`
}

// NewID 生成 12 位十六进制会话 ID。
func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// MakeTitle 从首条用户消息生成会话标题（按 rune 截断，中文完整）。
func MakeTitle(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	runes := []rune(t)
	if len(runes) > 24 {
		return string(runes[:24]) + "…"
	}
	if t == "" {
		return "新会话"
	}
	return t
}

// Save 保存（或创建）会话。workspace 记录工作目录（按目录过滤用）；
// notes 随会话持久化但不发给模型。
func Save(sessionID string, messages []msg.Msg, title, workspace string, notes []any) error {
	d, err := getDB()
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	migrateLegacy(d)
	if messages == nil {
		messages = []msg.Msg{}
	}
	arr := make([]any, len(messages))
	for i, m := range messages {
		arr[i] = m
	}
	return upsert(d, sessionID, title, arr, nil, nil, workspace, true, notes)
}

// Load 读会话（优先 SQLite，缺失则回退旧 JSON）。不存在/损坏返回 nil。
func Load(sessionID string) *Session {
	d, err := getDB()
	if err != nil {
		return nil
	}
	mu.Lock()
	migrateLegacy(d)
	mu.Unlock()
	var title string
	// created/updated 列为 REAL（兼容 Python 版 time.time() 浮点时间戳）：
	// 必须扫进 float64 再转整型，直接扫 int64 会因驱动返回 float64 而失败
	var createdF, updatedF float64
	var workspace, msgJSON string
	err = d.QueryRow(
		"SELECT title, created, updated, workspace, messages FROM sessions WHERE id=?",
		sessionID).Scan(&title, &createdF, &updatedF, &workspace, &msgJSON)
	if err == nil {
		created, updated := int64(createdF), int64(updatedF)
		var data struct {
			Messages []any `json:"messages"`
			Notes    []any `json:"notes"`
		}
		if json.Unmarshal([]byte(msgJSON), &data) == nil && data.Messages != nil {
			return &Session{
				ID: sessionID, Title: title, Created: created, Updated: updated,
				Workspace: workspace, Messages: data.Messages, Notes: data.Notes,
			}
		}
		// 兼容旧格式（纯 list）
		var legacyList []any
		if json.Unmarshal([]byte(msgJSON), &legacyList) == nil && legacyList != nil {
			return &Session{
				ID: sessionID, Title: title, Created: created, Updated: updated,
				Workspace: workspace, Messages: legacyList, Notes: []any{},
			}
		}
	}
	// 回退：读旧 JSON 文件（尚未迁移时兜底）
	raw, err := os.ReadFile(filepath.Join(config.LegacySessionsDir(), sessionID+".json"))
	if err != nil {
		return nil
	}
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil || msg.L(data, "messages") == nil {
		return nil
	}
	if data["notes"] == nil {
		data["notes"] = []any{}
	}
	return &Session{
		ID:        sessionID,
		Title:     msg.S(data, "title"),
		Created:   int64(msg.F(data, "created")),
		Updated:   int64(msg.F(data, "updated")),
		Workspace: msg.S(data, "workspace"),
		Messages:  msg.L(data, "messages"),
		Notes:     msg.L(data, "notes"),
	}
}

// Delete 删除会话记录（连同旧 JSON 文件）。
func Delete(sessionID string) bool {
	d, err := getDB()
	if err != nil {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	res, err := d.Exec("DELETE FROM sessions WHERE id=?", sessionID)
	_ = os.Remove(filepath.Join(config.LegacySessionsDir(), sessionID+".json"))
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// Rename 改会话标题。只更新 title，不刷新 updated（不扰动最近列表排序）。
// Move 迁移会话到另一个项目（工作区）：用于修正归错分组的会话。
// workspace 为空视为未指定，不移动。移动后下次 Load 该会话会自动切到新工作区。
func Move(sessionID, workspace string) bool {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return false
	}
	d, err := getDB()
	if err != nil {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	res, err := d.Exec("UPDATE sessions SET workspace=? WHERE id=?", workspace, sessionID)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			return true
		}
	}
	return false
}

func Rename(sessionID, title string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	d, err := getDB()
	if err != nil {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	res, err := d.Exec("UPDATE sessions SET title=? WHERE id=?", title, sessionID)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			return true
		}
	}
	// SQLite 无此记录时回退旧 JSON：读出后整体落库再改标题
	data := Load(sessionID)
	if data == nil {
		return false
	}
	created, updated := data.Created, data.Updated
	if err := upsert(d, sessionID, title, data.Messages, &created, &updated,
		data.Workspace, true, data.Notes); err != nil {
		return false
	}
	return true
}

// ListSessions 最近会话列表（按更新时间倒序）。
// workspace 非空时只返回该目录创建的会话；query 按标题/内容过滤。
func ListSessions(limit int, workspace, query string) []Meta {
	d, err := getDB()
	if err != nil {
		return nil
	}
	mu.Lock()
	migrateLegacy(d)
	mu.Unlock()
	sqlText := "SELECT id, title, updated, workspace FROM sessions"
	var conds []string
	var params []any
	if workspace != "" {
		conds = append(conds, "workspace=?")
		params = append(params, workspace)
	}
	if query != "" {
		q := "%" + strings.ToLower(query) + "%"
		conds = append(conds, "(lower(title) LIKE ? OR lower(messages) LIKE ?)")
		params = append(params, q, q)
	}
	if len(conds) > 0 {
		sqlText += " WHERE " + strings.Join(conds, " AND ")
	}
	sqlText += " ORDER BY updated DESC LIMIT ?"
	params = append(params, limit)
	rows, err := d.Query(sqlText, params...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Meta
	for rows.Next() {
		var id, title, ws string
		var updatedF float64
		if err := rows.Scan(&id, &title, &updatedF, &ws); err == nil {
			out = append(out, Meta{ID: id, Title: title, Updated: int64(updatedF), Workspace: ws})
		}
	}
	return out
}

// Close 关闭底层连接（测试隔离/应用退出用）。
func Close() {
	mu.Lock()
	if db != nil {
		_ = db.Close()
		db = nil
	}
	mu.Unlock()
}
