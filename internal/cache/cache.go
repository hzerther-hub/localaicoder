// Package cache LLM 响应 / 工具结果缓存：SQLite / 内存 两种后端
//（对译 Python cache.py）。键 = sha256(模型+请求内容)，TTL 过期即失效。
package cache

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（保持 CLI 静态构建）

	"localai/internal/config"
	"localai/internal/msg"
)

// ---------------- 设置 ----------------

var (
	settingsMu sync.Mutex // 注意：非可重入；持锁期间只调 loadSettingsLocked
	settings   map[string]any
)

func settingsFile() string { return filepath.Join(config.Dir(), "cache.json") }

func defaultSettings() map[string]any {
	return map[string]any{
		"backend":     "auto", // auto / sqlite / memory
		"sqlite_path": filepath.Join(config.Dir(), "cache.db"),
		"llm_ttl":     3600,
		"tool_ttl":    300,
	}
}

// loadSettingsLocked 读设置（调用方必须已持有 settingsMu）。
func loadSettingsLocked() map[string]any {
	if settings == nil {
		data := defaultSettings()
		if raw, err := os.ReadFile(settingsFile()); err == nil {
			var saved map[string]any
			if json.Unmarshal(raw, &saved) == nil {
				for k := range data {
					if v, ok := saved[k]; ok {
						data[k] = v
					}
				}
			}
		}
		settings = data
	}
	return settings
}

func cachedSettings() map[string]any {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return loadSettingsLocked()
}

// LoadSettings 读设置（返回副本），坏文件回退默认。
func LoadSettings() map[string]any {
	s := cachedSettings()
	out := make(map[string]any, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

// SaveSettings 更新并持久化设置；backend/sqlite 路径变化后自动重置连接。
func SaveSettings(kwargs map[string]any) map[string]any {
	settingsMu.Lock()
	old := loadSettingsLocked()
	s := make(map[string]any, len(old))
	for k, v := range old {
		s[k] = v
	}
	for k, v := range kwargs {
		if _, ok := defaultSettings()[k]; ok {
			s[k] = v
		}
	}
	_ = os.MkdirAll(config.Dir(), 0o755)
	_ = os.WriteFile(settingsFile(), mustJSONIndent(s), 0o644)
	settings = s
	settingsMu.Unlock()
	closeSQLite()
	return LoadSettings()
}

// Reset 重置全部缓存状态（测试隔离用）。
func Reset() {
	settingsMu.Lock()
	settings = nil
	settingsMu.Unlock()
	closeSQLite()
	memMu.Lock()
	memStore = map[string]memItem{}
	memMu.Unlock()
}

// ---------------- 键生成 ----------------

func hash(v any) string {
	b, _ := json.Marshal(v) // map 键排序，输出确定
	return sha256Hex(b)
}

// llmKey LLM 回复缓存键：模型 + 完整消息 + 工具 schema（请求时消息列表）。
func llmKey(modelID string, messages []msg.Msg, tools []msg.Msg) string {
	if tools == nil {
		tools = []msg.Msg{}
	}
	return "qwenc:llm:" + hash([]any{modelID, messages, tools})
}

func toolKey(name string, args map[string]any, workspace string) string {
	if args == nil {
		args = map[string]any{}
	}
	return "qwenc:tool:" + hash([]any{name, args, workspace})
}

// ---------------- 内存后端 ----------------

type memItem struct {
	expire float64
	val    string
}

var (
	memMu    sync.Mutex
	memStore = map[string]memItem{}
)

const memMax = 500

// ---------------- SQLite 后端 ----------------

var (
	sqliteMu   sync.Mutex
	sqliteDB   *sql.DB
	sqlitePath string
)

func closeSQLite() {
	sqliteMu.Lock()
	defer sqliteMu.Unlock()
	if sqliteDB != nil {
		_ = sqliteDB.Close()
		sqliteDB, sqlitePath = nil, ""
	}
}

func getSQLite() *sql.DB {
	sqliteMu.Lock()
	defer sqliteMu.Unlock()
	if sqliteDB != nil {
		return sqliteDB
	}
	path := msg.S(cachedSettings(), "sqlite_path")
	if path == "" {
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil
	}
	// 单连接串行化（对齐 Python 单连接 + 锁）
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		_ = db.Close()
		return nil
	}
	if _, err := db.Exec(
		"CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, expire_ts REAL, v TEXT)"); err != nil {
		_ = db.Close()
		return nil
	}
	sqliteDB, sqlitePath = db, path
	return db
}

func activeBackend() string {
	backend := msg.S(cachedSettings(), "backend")
	if backend == "memory" {
		return "memory"
	}
	if getSQLite() != nil {
		return "sqlite"
	}
	return "memory"
}

// ---------------- KV 接口 ----------------

// Get 取缓存；过期/不存在返回空串与 false。
func Get(key string) (string, bool) {
	backend := activeBackend()
	if backend == "sqlite" {
		db := getSQLite()
		if db == nil {
			return "", false
		}
		var expire float64
		var v string
		err := db.QueryRow("SELECT expire_ts, v FROM kv WHERE k = ?", key).Scan(&expire, &v)
		if err != nil {
			return "", false
		}
		if expire < float64(time.Now().UnixMilli())/1000 {
			_, _ = db.Exec("DELETE FROM kv WHERE k = ?", key)
			return "", false
		}
		return v, true
	}
	memMu.Lock()
	defer memMu.Unlock()
	item, ok := memStore[key]
	if !ok {
		return "", false
	}
	if item.expire < float64(time.Now().UnixMilli())/1000 {
		delete(memStore, key)
		return "", false
	}
	return item.val, true
}

// Put 写缓存。
func Put(key, value string, ttl int) {
	expire := float64(time.Now().UnixMilli())/1000 + float64(ttl)
	if activeBackend() == "sqlite" {
		db := getSQLite()
		if db == nil {
			return
		}
		_, _ = db.Exec(
			"INSERT OR REPLACE INTO kv (k, expire_ts, v) VALUES (?, ?, ?)",
			key, expire, value)
		return
	}
	memMu.Lock()
	defer memMu.Unlock()
	if len(memStore) >= memMax {
		// 淘汰最早过期的四分之一
		type kv struct {
			k string
			e float64
		}
		var all []kv
		for k, it := range memStore {
			all = append(all, kv{k, it.expire})
		}
		for i := 1; i < len(all); i++ {
			for j := i; j > 0 && all[j].e < all[j-1].e; j-- {
				all[j], all[j-1] = all[j-1], all[j]
			}
		}
		for _, x := range all[:memMax/4] {
			delete(memStore, x.k)
		}
	}
	memStore[key] = memItem{expire: expire, val: value}
}

// Clear 清空当前后端的全部缓存。
func Clear() bool {
	if activeBackend() == "sqlite" {
		db := getSQLite()
		if db == nil {
			return false
		}
		_, err := db.Exec("DELETE FROM kv")
		return err == nil
	}
	memMu.Lock()
	memStore = map[string]memItem{}
	memMu.Unlock()
	return true
}

// Stats 当前后端信息（诊断/管理界面用）。
func Stats() map[string]any {
	backend := activeBackend()
	info := map[string]any{
		"backend":    backend,
		"configured": msg.S(cachedSettings(), "backend"),
	}
	if backend == "sqlite" {
		db := getSQLite()
		if db == nil {
			info["entries"] = -1
		} else {
			var n int
			if err := db.QueryRow("SELECT COUNT(*) FROM kv").Scan(&n); err != nil {
				info["entries"] = -1
			} else {
				info["entries"] = n
			}
		}
	} else {
		memMu.Lock()
		info["entries"] = len(memStore)
		memMu.Unlock()
	}
	return info
}

// BackendName 当前实际生效的后端。
func BackendName() string { return activeBackend() }

// ---------------- 高层接口 ----------------

// DebugKey 暴露 LLM 缓存键（调试用）。
func DebugKey(modelID string, messages []msg.Msg, tools []msg.Msg) string {
	return llmKey(modelID, messages, tools)
}

// GetLLM 返回缓存的回复事件列表，未命中返回 nil。
func GetLLM(modelID string, messages []msg.Msg, tools []msg.Msg) []any {
	if msg.I(cachedSettings(), "llm_ttl") == 0 {
		return nil
	}
	raw, ok := Get(llmKey(modelID, messages, tools))
	if !ok {
		return nil
	}
	var events []any
	if json.Unmarshal([]byte(raw), &events) != nil {
		return nil
	}
	return events
}

// PutLLM 缓存一次纯文本回复的事件列表（不含工具调用轮）。
func PutLLM(modelID string, messages []msg.Msg, tools []msg.Msg, events []any) {
	ttl := msg.I(cachedSettings(), "llm_ttl")
	if ttl == 0 {
		return
	}
	Put(llmKey(modelID, messages, tools), string(mustJSONIndent(events)), ttl)
}

// GetTool 工具结果缓存。
func GetTool(name string, args map[string]any, workspace string) (string, bool) {
	if msg.I(cachedSettings(), "tool_ttl") == 0 {
		return "", false
	}
	return Get(toolKey(name, args, workspace))
}

// PutTool 写工具结果缓存。
func PutTool(name string, args map[string]any, workspace, result string) {
	ttl := msg.I(cachedSettings(), "tool_ttl")
	if ttl == 0 {
		return
	}
	Put(toolKey(name, args, workspace), result, ttl)
}

func mustJSONIndent(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return b
}

func sha256Hex(b []byte) string {
	return hexEncode(sha256Sum(b))
}
