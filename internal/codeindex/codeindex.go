// Package codeindex 代码库索引：解析 → 分块 → TF-IDF 向量化 → SQLite 可检索数据库
//（对译 Python codeindex.py；纯标准库 + modernc sqlite）。
//
// 特性：代码感知分词（camelCase/snake_case 拆分 + 中文 bigram）、
// TF-IDF 余弦相关度、按 workspace 哈希分库、按 mtime/size 增量更新。
package codeindex

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动

	"localai/internal/config"
)

// 分块参数
const (
	ChunkLines   = 50 // 每块行数
	ChunkOverlap = 10 // 相邻块重叠行数（保证边界上下文）
	MaxFileBytes = 1_000_000
)

// ExTS 支持的代码/文档扩展名。
var Exts = map[string]bool{
	".py": true, ".js": true, ".ts": true, ".jsx": true, ".tsx": true,
	".java": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true,
	".cs": true, ".go": true, ".rs": true, ".rb": true, ".php": true,
	".swift": true, ".kt": true, ".scala": true, ".sh": true, ".bash": true,
	".zsh": true, ".sql": true, ".html": true, ".css": true, ".scss": true,
	".vue": true, ".svelte": true, ".md": true, ".json": true,
	".yaml": true, ".yml": true, ".toml": true, ".cfg": true, ".ini": true,
}

// SkipDirs 索引跳过的目录。
var SkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true,
	"__pycache__": true, "venv": true, ".venv": true, "env": true,
	".tox": true, ".mypy_cache": true, ".pytest_cache": true, "dist": true,
	"build": true, "target": true, ".idea": true, ".vscode": true,
	".next": true, ".nuxt": true, "vendor": true, "bower_components": true,
}

// ---------------- 分词（代码感知） ----------------

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
var cjkRe = regexp.MustCompile(`[\x{4E00}-\x{9FFF}]+`)

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "not": true,
	"is": true, "are": true, "was": true, "were": true, "in": true,
	"on": true, "at": true, "to": true, "of": true, "for": true,
	"with": true, "by": true, "from": true, "as": true, "this": true,
	"that": true, "it": true, "be": true, "been": true, "has": true,
	"have": true, "had": true, "do": true, "does": true, "did": true,
	"if": true, "then": true, "else": true, "elif": true, "return": true,
	"def": true, "class": true, "import": true, "self": true, "true": true,
	"false": true, "none": true, "null": true, "void": true, "int": true,
	"str": true,
}

// Tokenize 把代码文本拆成检索词：标识符 + camelCase/snake_case 子词 + 中文 bigram。
func Tokenize(text string) []string {
	var words []string
	for _, ident := range identRe.FindAllString(text, -1) {
		low := strings.ToLower(ident)
		if stopwords[low] || len(low) < 2 {
			continue
		}
		words = append(words, low)
		if strings.Contains(ident, "_") { // snake_case → 子词
			for _, part := range strings.Split(ident, "_") {
				p := strings.ToLower(part)
				if len(p) >= 2 && p != low {
					words = append(words, p)
				}
			}
		}
		if hasCamelTail(ident) { // camelCase → 子词
			for _, part := range splitCamel(ident) {
				p := strings.ToLower(part)
				if len(p) >= 2 && p != low {
					words = append(words, p)
				}
			}
		}
	}
	for _, seg := range cjkRe.FindAllString(text, -1) { // 中文 → bigram
		runes := []rune(seg)
		words = append(words, string(runes[:2]))
		for i := 0; i+2 <= len(runes); i++ {
			words = append(words, string(runes[i:i+2]))
		}
	}
	return words
}

// hasCamelTail 标识符首字符后是否含大写（对齐 Python ident[1:] 检查）。
func hasCamelTail(ident string) bool {
	for _, r := range ident[1:] {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

// splitCamel 手工 camel 拆分（RE2 不支持前瞻，等价 Python
// [A-Z]+(?=[A-Z][a-z0-9]) | [A-Z]?[a-z0-9]+ | [A-Z]+(?![a-z0-9])）。
func splitCamel(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		j := i
		if s[j] >= 'A' && s[j] <= 'Z' {
			// 连续大写：后跟「大写+小写/数字」时止于倒数第二个
			k := j
			for k < len(s) && s[k] >= 'A' && s[k] <= 'Z' {
				k++
			}
			if k < len(s) && ((s[k] >= 'a' && s[k] <= 'z') || (s[k] >= '0' && s[k] <= '9')) {
				j = k - 1 // [A-Z]+(?=[A-Z][a-z0-9])
			} else {
				j = k // [A-Z]+(?![a-z0-9])
			}
		} else {
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z')) {
				j++
			}
		}
		if j == i {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

// ChunkLinesOf 把文件切成带重叠的行块；行号从 1 开始。
func ChunkLinesOf(n int) [][2]int {
	var out [][2]int
	step := ChunkLines - ChunkOverlap
	start := 0
	for start < n {
		end := start + ChunkLines
		if end > n {
			end = n
		}
		out = append(out, [2]int{start + 1, end})
		if end == n {
			break
		}
		start += step
	}
	return out
}

// ---------------- 数据库 ----------------

var (
	connMu sync.Mutex
	conns  = map[string]*sql.DB{}
)

func dbPath(workspace string) string {
	h := sha256Hex16(workspace)
	return filepath.Join(config.IndexDir(), h+".db")
}

func conn(workspace string) (*sql.DB, error) {
	p := dbPath(workspace)
	connMu.Lock()
	defer connMu.Unlock()
	if c, ok := conns[p]; ok {
		return c, nil
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	d, err := sql.Open("sqlite", p)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	if _, err := d.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = d.Close()
		return nil, err
	}
	if _, err := d.Exec(
		"CREATE TABLE IF NOT EXISTS files (" +
			" path TEXT PRIMARY KEY, mtime REAL, size INTEGER)"); err != nil {
		_ = d.Close()
		return nil, err
	}
	if _, err := d.Exec(
		"CREATE TABLE IF NOT EXISTS chunks (" +
			" file TEXT, start_line INTEGER, end_line INTEGER," +
			" content TEXT, terms TEXT)"); err != nil {
		_ = d.Close()
		return nil, err
	}
	if _, err := d.Exec("CREATE INDEX IF NOT EXISTS ix_chunks_file ON chunks(file)"); err != nil {
		_ = d.Close()
		return nil, err
	}
	conns[p] = d
	return d, nil
}

// CloseAll 关闭全部索引连接（测试隔离/应用退出用）。
func CloseAll() {
	connMu.Lock()
	defer connMu.Unlock()
	for p, c := range conns {
		_ = c.Close()
		delete(conns, p)
	}
}

// ---------------- 索引构建（增量） ----------------

// BuildStats 一次构建的统计。
type BuildStats struct {
	FilesIndexed     int     `json:"files_indexed"`
	Updated          int     `json:"updated"`
	SkippedUnchanged int     `json:"skipped_unchanged"`
	Seconds          float64 `json:"seconds"`
}

// Build 扫描 workspace 建索引；progress 可选回调 (done, total)。
func Build(workspace string, force bool, progress func(done, total int)) BuildStats {
	t0 := time.Now()
	workspace, _ = filepath.Abs(workspace)
	d, err := conn(workspace)
	if err != nil {
		return BuildStats{}
	}

	// 收集待索引文件
	var files []string
	_ = filepath.Walk(workspace, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if SkipDirs[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if Exts[strings.ToLower(filepath.Ext(info.Name()))] && info.Size() <= MaxFileBytes {
			files = append(files, p)
		}
		return nil
	})

	// 已索引且未变的文件
	known := map[string][2]float64{}
	rows, err := d.Query("SELECT path, mtime, size FROM files")
	if err == nil {
		for rows.Next() {
			var path string
			var mtime, size float64
			if rows.Scan(&path, &mtime, &size) == nil {
				known[path] = [2]float64{mtime, size}
			}
		}
		rows.Close()
	}

	type todoItem struct{ p, rel string; mtime, size float64 }
	var todo []todoItem
	valid := map[string]bool{}
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(workspace, p)
		rel = filepath.ToSlash(rel)
		valid[rel] = true
		if !force && known[rel] == [2]float64{float64(st.ModTime().UnixNano()) / 1e9, float64(st.Size())} {
			continue
		}
		todo = append(todo, todoItem{p, rel, float64(st.ModTime().UnixNano()) / 1e9, float64(st.Size())})
	}
	// 已删除的文件：清掉
	if rows, err := d.Query("SELECT path FROM files"); err == nil {
		var toDelete []string
		for rows.Next() {
			var path string
			if rows.Scan(&path) == nil && !valid[path] {
				toDelete = append(toDelete, path)
			}
		}
		rows.Close()
		for _, path := range toDelete {
			_, _ = d.Exec("DELETE FROM files WHERE path=?", path)
			_, _ = d.Exec("DELETE FROM chunks WHERE file=?", path)
		}
	}

	done := 0
	for _, item := range todo {
		data, err := os.ReadFile(item.p)
		if err != nil {
			continue
		}
		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		_, _ = d.Exec("DELETE FROM chunks WHERE file=?", item.rel)
		for _, se := range ChunkLinesOf(len(lines)) {
			content := strings.Join(lines[se[0]-1:se[1]], "\n")
			terms := Tokenize(content)
			if len(terms) == 0 {
				continue
			}
			tj, _ := json.Marshal(terms)
			_, _ = d.Exec(
				"INSERT INTO chunks (file, start_line, end_line, content, terms) VALUES (?, ?, ?, ?, ?)",
				item.rel, se[0], se[1], content, string(tj))
		}
		_, _ = d.Exec("INSERT OR REPLACE INTO files VALUES (?, ?, ?)",
			item.rel, item.mtime, item.size)
		done++
		if progress != nil && done%20 == 0 {
			progress(done, len(todo))
		}
	}
	return BuildStats{
		FilesIndexed:     len(files),
		Updated:          done,
		SkippedUnchanged: len(files) - len(todo),
		Seconds:          math.Round(time.Since(t0).Seconds()*100) / 100,
	}
}

// ---------------- 检索（TF-IDF 余弦） ----------------

// Hit 一个检索命中。
type Hit struct {
	File      string
	StartLine int
	EndLine   int
	Content   string
	Score     float64
}

type vecData struct {
	docs  []doc
	idf   map[string]float64
	norms []float64
}

type doc struct {
	file          string
	start, end    int
	content       string
	tf            map[string]int
}

var (
	searchMu    sync.Mutex
	searchCache = map[string]searchCacheEntry{}
)

type searchCacheEntry struct {
	at   time.Time
	data *vecData
}

func loadVectors(workspace string) *vecData {
	searchMu.Lock()
	defer searchMu.Unlock()
	if e, ok := searchCache[workspace]; ok && time.Since(e.at) < 10*time.Second {
		return e.data
	}
	d, err := conn(workspace)
	if err != nil {
		return &vecData{}
	}
	rows, err := d.Query("SELECT file, start_line, end_line, content, terms FROM chunks")
	if err != nil {
		return &vecData{}
	}
	defer rows.Close()
	df := map[string]int{}
	var docs []doc
	for rows.Next() {
		var file, content, termsJSON string
		var start, end int
		if rows.Scan(&file, &start, &end, &content, &termsJSON) != nil {
			continue
		}
		var terms []string
		if json.Unmarshal([]byte(termsJSON), &terms) != nil {
			continue
		}
		tf := map[string]int{}
		for _, t := range terms {
			tf[t]++
		}
		for t := range tf {
			df[t]++
		}
		docs = append(docs, doc{file, start, end, content, tf})
	}
	n := len(docs)
	if n == 0 {
		n = 1
	}
	idf := map[string]float64{}
	for t, dcnt := range df {
		idf[t] = math.Log(float64(n+1)/float64(dcnt+1)) + 1.0
	}
	norms := make([]float64, len(docs))
	for i, dc := range docs {
		s := 0.0
		for t, c := range dc.tf {
			w := 1 + math.Log(float64(c))
			s += w * w * idf[t] * idf[t]
		}
		norms[i] = math.Sqrt(s)
		if norms[i] == 0 {
			norms[i] = 1.0
		}
	}
	data := &vecData{docs, idf, norms}
	searchCache[workspace] = searchCacheEntry{time.Now(), data}
	return data
}

// Search 相关度检索，返回 top_k 个代码块。
func Search(workspace, query string, topK int) []Hit {
	v := loadVectors(workspace)
	if len(v.docs) == 0 {
		return nil
	}
	qTerms := Tokenize(query)
	if len(qTerms) == 0 {
		return nil
	}
	qTF := map[string]int{}
	for _, t := range qTerms {
		qTF[t]++
	}
	qWeights := map[string]float64{}
	for t, c := range qTF {
		idf := v.idf[t]
		qWeights[t] = (1 + math.Log(float64(c))) * idf
	}
	qs := 0.0
	for _, w := range qWeights {
		qs += w * w
	}
	qNorm := math.Sqrt(qs)
	if qNorm == 0 {
		qNorm = 1.0
	}
	var scored []Hit
	for i, dc := range v.docs {
		dot := 0.0
		for t, w := range qWeights {
			if c, ok := dc.tf[t]; ok && c > 0 {
				dot += w * (1 + math.Log(float64(c))) * v.idf[t]
			}
		}
		if dot <= 0 {
			continue
		}
		score := dot / (qNorm * v.norms[i])
		scored = append(scored, Hit{
			File: dc.file, StartLine: dc.start, EndLine: dc.end,
			Content: dc.content, Score: math.Round(score*10000) / 10000,
		})
	}
	sort.Slice(scored, func(a, b int) bool { return scored[a].Score > scored[b].Score })
	if len(scored) > topK {
		scored = scored[:topK]
	}
	return scored
}

// Stats 索引统计。
func Stats(workspace string) map[string]any {
	d, err := conn(workspace)
	if err != nil {
		return map[string]any{"files": 0, "chunks": 0, "db": dbPath(workspace)}
	}
	var files, chunks int
	_ = d.QueryRow("SELECT COUNT(*) FROM files").Scan(&files)
	_ = d.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&chunks)
	return map[string]any{"files": files, "chunks": chunks, "db": dbPath(workspace)}
}

// Ensure 确保索引存在（为空则构建），返回统计。
func Ensure(workspace string, progress func(done, total int)) map[string]any {
	s := Stats(workspace)
	if s["chunks"] == 0 {
		Build(workspace, false, progress)
		s = Stats(workspace)
	}
	return s
}
