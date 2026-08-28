// Package codera 公司多根知识库（企业代码 RAG）：N 个目录（代码 + 文档）
// → SQLite → TF-IDF + 可选 embedding 混合检索。对译 Python codera.py。
//
// 与工作区 codeindex 不同：多个固定根目录建成一个持久化、可配置的知识库，
// 跨会话复用；复用 codeindex 的分词/分块/常量，私建自己的库表。
package codera

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动

	"localai/internal/codeindex"
	"localai/internal/config"
	"localai/internal/embed"
)

// EmbedWeight 混合评分里向量余弦的权重。
const EmbedWeight = 0.6

// docExts 纯文档扩展名（代码扩展之外额外纳入）。
var docExts = map[string]bool{".txt": true, ".rst": true, ".adoc": true, ".tex": true}

func indexExts() map[string]bool {
	out := make(map[string]bool, len(codeindex.Exts)+len(docExts))
	for k := range codeindex.Exts {
		out[k] = true
	}
	for k := range docExts {
		out[k] = true
	}
	return out
}

const (
	cacheTTL       = 10 * time.Second
	autoRefreshTTL = 60 * time.Second // 检索前自动增量刷新节流秒数
)

var (
	mu               sync.Mutex
	conns            = map[string]*sql.DB{}
	searchCache      = map[string]searchEntry{}
	lastAutoRefresh  = map[string]time.Time{}
)

type searchEntry struct {
	at   time.Time
	data *vecData
}

// ---------------- 路径 / 库 ----------------

// RootsHash 按排序后的绝对路径生成库标识（同一组根目录共享一个库）。
func RootsHash(roots []string) string {
	var abs []string
	for _, r := range roots {
		a, err := filepath.Abs(r)
		if err == nil {
			abs = append(abs, a)
		}
	}
	sort.Strings(abs)
	key, _ := json.Marshal(abs)
	return sha256Hex16(string(key))
}

func dbPath(roots []string) string {
	return filepath.Join(config.KBDir(), RootsHash(roots)+".db")
}

func conn(roots []string) (*sql.DB, error) {
	p := dbPath(roots)
	mu.Lock()
	defer mu.Unlock()
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
	stmts := []string{
		"CREATE TABLE IF NOT EXISTS files (" +
			" root TEXT, path TEXT, mtime REAL, size INTEGER," +
			" PRIMARY KEY (root, path))",
		"CREATE TABLE IF NOT EXISTS chunks (" +
			" id INTEGER PRIMARY KEY AUTOINCREMENT," +
			" root TEXT, file TEXT, start_line INTEGER, end_line INTEGER," +
			" content TEXT, terms TEXT)",
		"CREATE TABLE IF NOT EXISTS emb (id INTEGER PRIMARY KEY, dim INTEGER, vec BLOB)",
		"CREATE INDEX IF NOT EXISTS ix_chunks_file ON chunks(file)",
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			_ = d.Close()
			return nil, err
		}
	}
	conns[p] = d
	return d, nil
}

// ---------------- 文件收集 ----------------

type fileItem struct{ root, rel, path string }

func iterFiles(roots []string) []fileItem {
	exts := indexExts()
	var out []fileItem
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			continue
		}
		_ = filepath.Walk(abs, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				if codeindex.SkipDirs[info.Name()] || strings.HasPrefix(info.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !exts[strings.ToLower(filepath.Ext(info.Name()))] {
				return nil
			}
			if info.Size() > codeindex.MaxFileBytes {
				return nil
			}
			rel, _ := filepath.Rel(abs, p)
			out = append(out, fileItem{abs, filepath.ToSlash(rel), p})
			return nil
		})
	}
	return out
}

// ---------------- 索引构建（增量） ----------------

// BuildStats 一次构建统计。
type BuildStats struct {
	FilesIndexed     int     `json:"files_indexed"`
	Updated          int     `json:"updated"`
	SkippedUnchanged int     `json:"skipped_unchanged"`
	Embedding        string  `json:"embedding"`
	Seconds          float64 `json:"seconds"`
}

// Build 扫描 roots 建索引，返回统计；progress 可选回调 (done, total)。
func Build(roots []string, force bool, progress func(done, total int)) BuildStats {
	t0 := time.Now()
	if len(roots) == 0 {
		return BuildStats{Embedding: "tfidf"}
	}
	d, err := conn(roots)
	if err != nil {
		return BuildStats{Embedding: "tfidf"}
	}
	files := iterFiles(roots)

	// 已索引且未变的文件
	type sig struct{ mtime, size float64 }
	known := map[string]sig{} // key: root + "\x00" + rel
	rows, err := d.Query("SELECT root, path, mtime, size FROM files")
	if err == nil {
		for rows.Next() {
			var root, path string
			var mtime, size float64
			if rows.Scan(&root, &path, &mtime, &size) == nil {
				known[root+"\x00"+path] = sig{mtime, size}
			}
		}
		rows.Close()
	}

	type todoItem struct {
		fileItem
		s sig
	}
	var todo []todoItem
	valid := map[string]bool{}
	for _, f := range files {
		st, err := os.Stat(f.path)
		if err != nil {
			continue
		}
		key := f.root + "\x00" + f.rel
		valid[key] = true
		s := sig{float64(st.ModTime().UnixNano()) / 1e9, float64(st.Size())}
		if !force && known[key] == s {
			continue
		}
		todo = append(todo, todoItem{f, s})
	}
	// 已不在候选里的文件（删除/改名/超限）：清掉对应块与向量
	if rows, err := d.Query("SELECT root, path FROM files"); err == nil {
		type rp struct{ root, path string }
		var gone []rp
		for rows.Next() {
			var root, path string
			if rows.Scan(&root, &path) == nil && !valid[root+"\x00"+path] {
				gone = append(gone, rp{root, path})
			}
		}
		rows.Close()
		for _, g := range gone {
			var ids []int64
			if r2, err := d.Query("SELECT id FROM chunks WHERE root=? AND file=?", g.root, g.path); err == nil {
				for r2.Next() {
					var id int64
					if r2.Scan(&id) == nil {
						ids = append(ids, id)
					}
				}
				r2.Close()
			}
			for _, id := range ids {
				_, _ = d.Exec("DELETE FROM emb WHERE id=?", id)
			}
			_, _ = d.Exec("DELETE FROM chunks WHERE root=? AND file=?", g.root, g.path)
			_, _ = d.Exec("DELETE FROM files WHERE root=? AND path=?", g.root, g.path)
		}
	}

	// embedding 增强：配置了模型才尝试；任一块失败则整批跳过（退回纯 TF-IDF）
	var embModel *config.ModelConfig
	if key := config.GetKBEmbedding(); key != "" {
		embModel = config.FindModel(key)
	}
	useEmbed := embModel != nil
	embFailed := false

	done := 0
	for _, item := range todo {
		data, err := os.ReadFile(item.path)
		if err != nil {
			continue
		}
		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		// 先清该文件旧块（重写）
		var oldIDs []int64
		if r2, err := d.Query("SELECT id FROM chunks WHERE root=? AND file=?", item.root, item.rel); err == nil {
			for r2.Next() {
				var id int64
				if r2.Scan(&id) == nil {
					oldIDs = append(oldIDs, id)
				}
			}
			r2.Close()
		}
		for _, id := range oldIDs {
			_, _ = d.Exec("DELETE FROM emb WHERE id=?", id)
		}
		_, _ = d.Exec("DELETE FROM chunks WHERE root=? AND file=?", item.root, item.rel)
		for _, se := range codeindex.ChunkLinesOf(len(lines)) {
			content := strings.Join(lines[se[0]-1:se[1]], "\n")
			terms := codeindex.Tokenize(content)
			if len(terms) == 0 {
				continue
			}
			tj, _ := json.Marshal(terms)
			res, err := d.Exec(
				"INSERT INTO chunks (root, file, start_line, end_line, content, terms)"+
					" VALUES (?, ?, ?, ?, ?, ?)",
				item.root, item.rel, se[0], se[1], content, string(tj))
			if useEmbed && !embFailed && err == nil && embModel != nil {
				cid, _ := res.LastInsertId()
				vecs := embed.Embed(*embModel, []string{content})
				if len(vecs) == 1 && len(vecs[0]) > 0 {
					_, _ = d.Exec("INSERT OR REPLACE INTO emb (id, dim, vec) VALUES (?, ?, ?)",
						cid, len(vecs[0]), packVec(vecs[0]))
				} else {
					useEmbed = false // 后续块不再尝试
					embFailed = true
				}
			}
		}
		_, _ = d.Exec("INSERT OR REPLACE INTO files (root, path, mtime, size) VALUES (?, ?, ?, ?)",
			item.root, item.rel, item.s.mtime, item.s.size)
		done++
		if progress != nil && done%20 == 0 {
			progress(done, len(todo))
		}
	}
	mu.Lock()
	delete(searchCache, RootsHash(roots)) // 数据变了，作废向量缓存
	mu.Unlock()
	mode := "hybrid"
	if !useEmbed {
		mode = "tfidf"
	}
	return BuildStats{
		FilesIndexed: len(files), Updated: done,
		SkippedUnchanged: len(files) - len(todo),
		Embedding: mode,
		Seconds:   math.Round(time.Since(t0).Seconds()*100) / 100,
	}
}

// Ensure 确保索引存在（为空则构建），返回统计。
func Ensure(roots []string, progress func(done, total int)) map[string]any {
	if len(roots) == 0 {
		return map[string]any{"files": 0, "chunks": 0, "db": ""}
	}
	s := Stats(roots)
	if s["chunks"] == 0 {
		Build(roots, false, progress)
		s = Stats(roots)
	}
	return s
}

// MaybeAutoRefresh 检索路径懒调用的自动增量刷新（kb_search / 自动注入检前调用）。
// kb_auto 关闭 → 只在索引为空时构建；开启且距上次增量 < 60s → 跳过。
func MaybeAutoRefresh(roots []string) BuildStats {
	key := RootsHash(roots)
	if config.GetKBAuto() {
		mu.Lock()
		last, has := lastAutoRefresh[key]
		mu.Unlock()
		if has && time.Since(last) < autoRefreshTTL {
			s := Stats(roots)
			return BuildStats{
				FilesIndexed: s["files"].(int), Updated: 0,
				SkippedUnchanged: s["files"].(int), Embedding: "skip",
			}
		}
	}
	r := Build(roots, false, nil)
	if config.GetKBAuto() {
		mu.Lock()
		lastAutoRefresh[key] = time.Now()
		mu.Unlock()
	}
	return r
}

// ---------------- 向量存取 ----------------

func packVec(v []float64) []byte {
	b := make([]byte, 8*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint64(b[i*8:], math.Float64bits(x))
	}
	return b
}

func unpackVec(b []byte) []float64 {
	n := len(b) / 8
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = math.Float64frombits(binary.LittleEndian.Uint64(b[i*8:]))
	}
	return out
}

func embedModel() *config.ModelConfig {
	key := config.GetKBEmbedding()
	if key == "" {
		return nil
	}
	return config.FindModel(key)
}

// ---------------- 检索（TF-IDF 余弦 + 可选 embedding 混合） ----------------

// Hit 一个检索命中。
type Hit struct {
	Root      string
	File      string
	StartLine int
	EndLine   int
	Content   string
	Score     float64
	Source    string // "tfidf" / "hybrid"
}

type kbDoc struct {
	id                              int64
	root, file                      string
	start, end                      int
	content                         string
	tf                              map[string]int
}

type vecData struct {
	docs  []kbDoc
	idf   map[string]float64
	norms []float64
}

func loadVectors(roots []string) *vecData {
	key := RootsHash(roots)
	mu.Lock()
	if e, ok := searchCache[key]; ok && time.Since(e.at) < cacheTTL {
		defer mu.Unlock()
		return e.data
	}
	mu.Unlock()
	d, err := conn(roots)
	if err != nil {
		return &vecData{idf: map[string]float64{}}
	}
	rows, err := d.Query("SELECT id, root, file, start_line, end_line, content, terms FROM chunks")
	if err != nil {
		return &vecData{idf: map[string]float64{}}
	}
	defer rows.Close()
	df := map[string]int{}
	var docs []kbDoc
	for rows.Next() {
		var cid int64
		var root, file, content, termsJSON string
		var start, end int
		if rows.Scan(&cid, &root, &file, &start, &end, &content, &termsJSON) != nil {
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
		docs = append(docs, kbDoc{cid, root, file, start, end, content, tf})
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
			idfT := idf[t]
			s += w * w * idfT * idfT
		}
		norms[i] = math.Sqrt(s)
		if norms[i] == 0 {
			norms[i] = 1.0
		}
	}
	data := &vecData{docs, idf, norms}
	mu.Lock()
	searchCache[key] = searchEntry{time.Now(), data}
	mu.Unlock()
	return data
}

func loadEmb(roots []string) map[int64][]float64 {
	d, err := conn(roots)
	if err != nil {
		return nil
	}
	rows, err := d.Query("SELECT id, vec FROM emb")
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[int64][]float64{}
	for rows.Next() {
		var id int64
		var vec []byte
		if rows.Scan(&id, &vec) == nil && len(vec) > 0 {
			out[id] = unpackVec(vec)
		}
	}
	return out
}

type scoredItem struct {
	score float64
	doc   kbDoc
}

// Search 相关度检索，返回 top_k 个代码/文档块；roots 空时取配置的根目录。
func Search(query string, topK int, roots []string) []Hit {
	if len(roots) == 0 {
		roots = config.GetKBRoots()
	}
	if len(roots) == 0 {
		return nil
	}
	if topK <= 0 {
		topK = config.GetKBTopK()
	}
	v := loadVectors(roots)
	if len(v.docs) == 0 {
		return nil
	}
	qTerms := codeindex.Tokenize(query)
	if len(qTerms) == 0 {
		return nil
	}
	qTF := map[string]int{}
	for _, t := range qTerms {
		qTF[t]++
	}
	qWeights := map[string]float64{}
	for t, c := range qTF {
		qWeights[t] = (1 + math.Log(float64(c))) * v.idf[t]
	}
	qs := 0.0
	for _, w := range qWeights {
		qs += w * w
	}
	qNorm := math.Sqrt(qs)
	if qNorm == 0 {
		qNorm = 1.0
	}

	type si = scoredItem
	var scored []si
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
		scored = append(scored, si{dot / (qNorm * v.norms[i]), dc})
	}

	// embedding 混合：配置了模型 + 有落库向量 + 维度一致 → 加分
	source := "tfidf"
	extra := hybridExtra(query, scored, roots)
	tfMax := 0.0
	if len(extra) > 0 {
		source = "hybrid"
		for _, s := range scored {
			if s.score > tfMax {
				tfMax = s.score
			}
		}
		if tfMax == 0 {
			tfMax = 1.0
		}
	}
	var out []Hit
	for _, s := range scored {
		total := s.score/tfMax + extra[s.doc.id]
		out = append(out, Hit{
			Root: s.doc.root, File: s.doc.file,
			StartLine: s.doc.start, EndLine: s.doc.end,
			Content: s.doc.content,
			Score:   math.Round(total*10000) / 10000,
			Source:  source,
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if len(out) > topK {
		out = out[:topK]
	}
	return out
}

func hybridExtra(query string, scored []scoredItem, roots []string) map[int64]float64 {
	if len(scored) == 0 {
		return nil
	}
	model := embedModel()
	if model == nil {
		return nil
	}
	emb := loadEmb(roots)
	if len(emb) == 0 {
		return nil
	}
	qvecs := embed.Embed(*model, []string{query})
	if len(qvecs) != 1 {
		return nil
	}
	qvec := qvecs[0]
	refDim := 0
	for _, v := range emb {
		refDim = len(v)
		break
	}
	if refDim == 0 || len(qvec) != refDim {
		return nil // 维度不一致 → 退回纯 TF-IDF
	}
	qn := 0.0
	for _, x := range qvec {
		qn += x * x
	}
	qn = math.Sqrt(qn)
	if qn == 0 {
		qn = 1.0
	}
	extra := map[int64]float64{}
	for _, s := range scored {
		v := emb[s.doc.id]
		if v == nil {
			continue
		}
		dot := 0.0
		vn := 0.0
		for i := range qvec {
			dot += qvec[i] * v[i]
			vn += v[i] * v[i]
		}
		vn = math.Sqrt(vn)
		if vn == 0 {
			continue
		}
		cos := dot / (qn * vn)
		extra[s.doc.id] = EmbedWeight * (cos + 1.0) / 2.0 // 归一到 [0,1]
	}
	return extra
}

// ---------------- RAG 上下文 ----------------

// RetrieveContext 把检索结果格式化成一段可直接注入模型上下文的文本块。
func RetrieveContext(query string, topK int, maxChars int, roots []string) string {
	if maxChars <= 0 {
		maxChars = 4000
	}
	hits := Search(query, topK, roots)
	if len(hits) == 0 {
		return ""
	}
	lines := []string{"📚 公司知识库相关片段（检索自企业代码/文档，仅作参考）："}
	for _, h := range hits {
		lines = append(lines, fmtSprintf("### [%s 相关度 %.4f] %s/%s:%d-%d",
			h.Source, h.Score, h.Root, h.File, h.StartLine, h.EndLine))
		lines = append(lines, h.Content)
	}
	out := strings.Join(lines, "\n\n")
	if len(out) <= maxChars {
		return out
	}
	return out[:maxChars] + "\n…[片段已截断]"
}

// Stats 索引统计。
func Stats(roots []string) map[string]any {
	if len(roots) == 0 {
		return map[string]any{"files": 0, "chunks": 0, "db": ""}
	}
	d, err := conn(roots)
	if err != nil {
		return map[string]any{"files": 0, "chunks": 0, "db": dbPath(roots)}
	}
	var files, chunks int
	_ = d.QueryRow("SELECT COUNT(*) FROM files").Scan(&files)
	_ = d.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&chunks)
	return map[string]any{"files": files, "chunks": chunks, "db": dbPath(roots)}
}
