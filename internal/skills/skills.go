// Package skills 技能系统（对齐 Python 版 skills.py + skills_distill.py）：
// 把成功会话里的可复用经验（流程/坑点/平台 API 映射）沉淀为带 frontmatter 的
// Markdown 技能，按需注入 system prompt；会话结束后可蒸馏为【草稿】，
// 由人工确认转正（LLM 只产草稿，人握转正权）。
//
// 三个作用域目录：
//   - 用户级：CONFIG_DIR/skills/（跨项目生效）
//   - 项目级：<workspace>/.localai-skills/（随工作区）
//   - 草稿：CONFIG_DIR/skills/drafts/（蒸馏产出，待确认）
//
// 载体格式（严格 frontmatter）：
//
//	---
//	name: quant-joinquant-to-qmt
//	description: 一句话说明这条经验
//	when: 触发词1,触发词2
//	---
//	正文：精炼流程/坑/映射表（≤400 字，只要可执行结论）
package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"localai/internal/config"
)

// Skill 一条可复用技能经验。
type Skill struct {
	Name        string `json:"name"`        // 规范名（小写连字符）
	Description string `json:"description"` // 一句话说明
	When        string `json:"when"`        // 触发词（逗号分隔）
	Body        string `json:"body"`        // 正文（可执行结论）
	Path        string `json:"path"`        // 文件路径
}

// 作用域常量（Save 用）。
const (
	ScopeUser    = "user"
	ScopeProject = "project"
	ScopeDraft   = "draft"
)

// maxBodyChars 解析时正文截断上限（防御性，蒸馏后处理也复用）。
const maxBodyChars = 2000

// UserDir 用户级技能目录。
func UserDir() string { return filepath.Join(config.Dir(), "skills") }

// DraftsDir 蒸馏草稿目录。
func DraftsDir() string { return filepath.Join(config.Dir(), "skills", "drafts") }

// ProjectDir 项目级技能目录（随工作区）。
func ProjectDir(workspace string) string {
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, ".localai-skills")
}

// truncateBytes 按字节上限截断，但保证结果仍是合法 UTF-8：
// 若 maxBytes 恰好落在多字节 rune 中间，则回退到上一个完整 rune 边界。
// 正文常含中文，直接切片会产生无效字节序列（写入文件后显示乱码）。
func truncateBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// CleanName 规范技能名：小写、空格/下划线转连字符、剔除非法字符、收敛连续连字符。
func CleanName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
	}
	s = b.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// parseSkillText 解析技能 Markdown 文本（严格 frontmatter，首行必须是 ---）。
func parseSkillText(text string) (Skill, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) || strings.TrimSpace(lines[i]) != "---" {
		return Skill{}, errNoFrontmatter
	}
	i++
	sk := Skill{}
	closed := false
	for ; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			i++
			closed = true
			break
		}
		line := strings.TrimSpace(lines[i])
		k, v, ok := cutField(line)
		if !ok {
			continue
		}
		switch k {
		case "name":
			sk.Name = v
		case "description":
			sk.Description = v
		case "when":
			sk.When = v
		}
	}
	if !closed {
		return Skill{}, errNoFrontmatter
	}
	sk.Body = truncateBytes(strings.TrimSpace(strings.Join(lines[i:], "\n")), maxBodyChars)
	return sk, nil
}

// cutField 拆 "key: value"（值可为空）。
func cutField(line string) (string, string, bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(line[:idx])), strings.TrimSpace(line[idx+1:]), true
}

// Render 序列化为技能 Markdown（保存格式，与解析互逆）。
func (sk Skill) Render() string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + sk.Name + "\n")
	b.WriteString("description: " + sk.Description + "\n")
	b.WriteString("when: " + sk.When + "\n")
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(sk.Body) + "\n")
	return b.String()
}

var errNoFrontmatter = errStatic{}

type errStatic struct{}

func (errStatic) Error() string { return "缺少 frontmatter（首行必须是 ---）" }

// ---------------- 载入（mtime 快照缓存） ----------------

type snapshot struct {
	mtimes map[string]int64
	skills []Skill
}

var (
	cacheMu   sync.Mutex
	userSnap  *snapshot
	projSnaps = map[string]*snapshot{} // workspace -> snapshot
)

// ExternalDirs 外部技能源（Claude Code / OpenCode 的技能目录约定：
// skills/<name>/SKILL.md，递归扫描）。返回 [来源标签, 目录] 列表；
// 外部技能只读——不参与本工具的编辑/删除/转正（防破坏其它工具的数据）。
func ExternalDirs(workspace string) [][2]string {
	home, err := os.UserHomeDir()
	out := [][2]string{}
	if err == nil && home != "" {
		out = append(out,
			[2]string{"claude", filepath.Join(home, ".claude", "skills")},
			[2]string{"opencode", filepath.Join(home, ".config", "opencode", "skill")},
		)
	}
	if workspace != "" {
		out = append(out,
			[2]string{"claude", filepath.Join(workspace, ".claude", "skills")},
			[2]string{"opencode", filepath.Join(workspace, ".opencode", "skill")},
		)
	}
	return out
}

// loadExternalLocked 递归扫描一个外部技能目录（深度 ≤3），收集 SKILL.md；
// 同名技能以本工具自有技能优先（外部只补充，不覆盖）。
func loadExternalLocked(dir string, byName map[string]Skill) {
	if dir == "" {
		return
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.Count(p[len(dir):], string(os.PathSeparator)) > 3 {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if name != "SKILL.md" && name != "skill.md" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		sk, err := parseSkillText(string(data))
		if err != nil {
			return nil // frontmatter 不合法的外部技能静默跳过
		}
		if sk.Name == "" {
			sk.Name = CleanName(filepath.Base(filepath.Dir(p)))
		}
		if sk.Name == "" {
			return nil
		}
		sk.Path = p
		if _, exists := byName[sk.Name]; !exists {
			byName[sk.Name] = sk
		}
		return nil
	})
}

// LoadAll 载入用户级 + 项目级技能（同名时项目级覆盖用户级），按名称排序。
// 额外递归扫描外部技能源（Claude Code / OpenCode），只补充不覆盖。
func LoadAll(workspace string) []Skill {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	return loadAllLocked(workspace)
}

func loadAllLocked(workspace string) []Skill {
	byName := map[string]Skill{}
	userSnap = loadDirLocked(UserDir(), userSnap, byName)
	if pd := ProjectDir(workspace); pd != "" {
		projSnaps[workspace] = loadDirLocked(pd, projSnaps[workspace], byName)
	}
	for _, src := range ExternalDirs(workspace) {
		loadExternalLocked(src[1], byName)
	}
	out := make([]Skill, 0, len(byName))
	for _, sk := range byName {
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// loadDirLocked 扫描目录；mtime 未变直接复用快照，变更则重读。
func loadDirLocked(dir string, snap *snapshot, byName map[string]Skill) *snapshot {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if snap != nil {
			return snap // 目录暂不可读（如工作区未挂载）保留旧快照
		}
		return &snapshot{mtimes: map[string]int64{}}
	}
	next := &snapshot{mtimes: map[string]int64{}, skills: nil}
	changed := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		next.mtimes[p] = info.ModTime().UnixNano()
		if snap == nil || snap.mtimes[p] != info.ModTime().UnixNano() {
			changed = true
		}
	}
	if !changed && snap != nil && len(snap.mtimes) == len(next.mtimes) {
		for _, sk := range snap.skills {
			byName[sk.Name] = sk
		}
		return snap
	}
	next.skills = nil
	for p := range next.mtimes {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sk, err := parseSkillText(string(data))
		if err != nil {
			continue // 格式不合法的文件静默跳过
		}
		if sk.Name == "" {
			sk.Name = CleanName(strings.TrimSuffix(filepath.Base(p), ".md"))
		}
		sk.Path = p
		next.skills = append(next.skills, sk)
		byName[sk.Name] = sk
	}
	return next
}

// ListDrafts 列蒸馏草稿（按修改时间新→旧）。
func ListDrafts() []Skill {
	entries, err := os.ReadDir(DraftsDir())
	if err != nil {
		return nil
	}
	type dated struct {
		sk Skill
		t  int64
	}
	var out []dated
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(DraftsDir(), e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sk, err := parseSkillText(string(data))
		if err != nil {
			continue
		}
		if sk.Name == "" {
			sk.Name = CleanName(strings.TrimSuffix(e.Name(), ".md"))
		}
		sk.Path = p
		out = append(out, dated{sk, info.ModTime().UnixNano()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].t > out[j].t })
	skills := make([]Skill, len(out))
	for i, d := range out {
		skills[i] = d.sk
	}
	return skills
}

// ParseText 解析技能 Markdown 文本（导出供桌面端保存前校验格式完整性）。
func ParseText(text string) (Skill, error) { return parseSkillText(text) }

// LoadFile 从指定路径载入技能（面板"载入编辑"用，草稿/正式通用）。
func LoadFile(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	sk, err := parseSkillText(string(data))
	if err != nil {
		return Skill{}, err
	}
	sk.Path = path
	return sk, nil
}

// Save 保存技能到指定作用域，返回落盘路径。
func Save(sk Skill, scope, workspace string) (string, error) {
	sk.Name = CleanName(sk.Name)
	if sk.Name == "" {
		sk.Name = "skill"
	}
	var dir string
	switch scope {
	case ScopeProject:
		dir = ProjectDir(workspace)
	case ScopeDraft:
		dir = DraftsDir()
	default:
		dir = UserDir()
	}
	if dir == "" {
		dir = UserDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, sk.Name+".md")
	if err := os.WriteFile(p, []byte(sk.Render()), 0o644); err != nil {
		return "", err
	}
	if scope != ScopeDraft {
		cacheMu.Lock()
		if scope == ScopeProject && workspace != "" {
			delete(projSnaps, workspace) // 项目级直接失效该工作区快照
		} else {
			userSnap = nil
		}
		cacheMu.Unlock()
	}
	return p, nil
}

// Remove 删除技能/草稿文件。
func Remove(path string) error {
	if path == "" || !strings.HasSuffix(path, ".md") {
		return os.ErrInvalid
	}
	cacheMu.Lock()
	userSnap = nil
	cacheMu.Unlock()
	return os.Remove(path)
}

// SimilarExisting 是否已有近似技能（同名或同描述，含草稿）——蒸馏查重用。
func SimilarExisting(workspace string, sk Skill) bool {
	for _, ex := range LoadAll(workspace) {
		if ex.Name == sk.Name || normalizeDesc(ex.Description) == normalizeDesc(sk.Description) {
			return true
		}
	}
	for _, ex := range ListDrafts() {
		if ex.Name == sk.Name || normalizeDesc(ex.Description) == normalizeDesc(sk.Description) {
			return true
		}
	}
	return false
}

var descSpaceRe = regexp.MustCompile(`\s+`)

// normalizeDesc 描述归一（查重比较用）。
func normalizeDesc(s string) string {
	return strings.TrimSpace(descSpaceRe.ReplaceAllString(s, ""))
}

// ---------------- system prompt 注入 ----------------

// maxInject 单次注入技能数上限（防提示词膨胀）。
const maxInject = 6

// PromptSection 返回注入 system prompt 动态区的技能段；无可用技能返回空串。
// userText 非空时按相关性排序（显式 when 命中 > 名称/描述自动推导命中，
// 同分保持名称序），最多 maxInject 条——导入的技能不手填 when 也可用。
func PromptSection(workspace, userText string) string {
	if !config.GetSkillsEnabled() {
		return ""
	}
	all := LoadAll(workspace)
	if len(all) == 0 {
		return ""
	}
	// 相关性排序（稳定：同分保持名称序）
	ut := strings.ToLower(userText)
	sort.SliceStable(all, func(i, j int) bool {
		return skillScore(all[i], ut) > skillScore(all[j], ut)
	})
	if len(all) > maxInject {
		all = all[:maxInject]
	}
	var b strings.Builder
	b.WriteString("# 可复用技能经验（历史上验证有效的做法，与本任务相关时优先遵循）")
	for _, sk := range all {
		b.WriteString("\n\n## " + sk.Name + " — " + sk.Description)
		if sk.When != "" {
			b.WriteString("（触发：" + sk.When + "）")
		}
		b.WriteString("\n" + sk.Body)
	}
	return b.String()
}

// hitsSkill 用户文本是否命中技能显式触发词（when）。
func hitsSkill(sk Skill, lowerUserText string) bool {
	for _, w := range splitTriggers(sk.When) {
		if strings.Contains(lowerUserText, w) {
			return true
		}
	}
	return false
}

// splitTriggers 拆逗号分隔触发词（小写、去空白、去空项）。
func splitTriggers(s string) []string {
	var out []string
	for _, w := range strings.Split(s, ",") {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// ---------------- 触发词自动推导（导入技能免手填 when） ----------------

// 相关性权重：显式 when 命中 > 名称词命中 > 描述/正文推导词命中 > 0（0 分按名称序补位）。
const (
	scoreWhenHit  = 4
	scoreNameHit  = 3
	scoreWordHit  = 2
	minWordLen    = 4  // 描述/正文推导词最小长度（3 字母词噪声大，如 log/web）
	minNameLen    = 3  // 名称分词最小长度（mcp/pdf 等标识词保留）
)

// deriveStopWords 推导触发词时剔除的高频泛词（几乎所有描述/正文都含，无区分度）。
var deriveStopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true,
	"when": true, "whenever": true, "while": true, "user": true, "want": true,
	"wants": true, "use": true, "used": true, "using": true, "any": true, "are": true,
	"can": true, "will": true, "must": true, "should": true, "not": true, "new": true,
	"from": true, "into": true, "out": true, "all": true, "has": true, "have": true,
	"had": true, "its": true, "they": true, "them": true, "their": true, "you": true,
	"your": true, "our": true, "one": true, "two": true, "than": true, "then": true,
	"also": true, "more": true, "most": true, "such": true, "some": true, "what": true,
	"which": true, "how": true, "why": true, "where": true, "who": true, "mean": true,
	"file": true, "files": true, "skill": true, "skills": true,
	"task": true, "tasks": true, "tool": true, "tools": true, "kit": true,
	"set": true, "input": true, "output": true, "based": true, "kind": true,
}

// asciiWordRe 提取小写英文词（≥3 字母），用于推导与用户文本分词。
var asciiWordRe = regexp.MustCompile(`[a-z]{3,}`)

// deriveNameWords 技能名分词（连字符/下划线拆开，小写、排序）。
// 名称是技能标识：mcp/pdf/pptx 这类 3~4 字母标识词也保留。
func deriveNameWords(name string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' }) {
		if len(p) >= minNameLen {
			out = append(out, strings.ToLower(p))
		}
	}
	sort.Strings(out)
	return out
}

// deriveWords 从描述与正文自动推导触发词（导入的技能不填 when 也能被匹配）：
// 英文词剥尾部 s、滤停用词（剥前与剥后都查，防 this→thi 之类变形残留）。
// 推导词只用于打分，不进 prompt；排序输出保证确定性。
func deriveWords(sk Skill) []string {
	seen := map[string]bool{}
	for _, w := range asciiWordRe.FindAllString(strings.ToLower(sk.Description+" "+sk.Body), -1) {
		if deriveStopWords[w] {
			continue
		}
		if len(w) > 3 && strings.HasSuffix(w, "s") {
			w = w[:len(w)-1]
		}
		if len(w) >= minWordLen && !deriveStopWords[w] {
			seen[w] = true
		}
	}
	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// skillScore 用户文本与技能的相关性分：显式 when 命中最强，名称词次之，
// 描述/正文推导词再次；用户文本里的英文短词按前缀匹配名称词（如 ppt→pptx）。
func skillScore(sk Skill, lowerUserText string) int {
	score := 0
	for _, w := range splitTriggers(sk.When) {
		if strings.Contains(lowerUserText, w) {
			score += scoreWhenHit
		}
	}
	nameWords := deriveNameWords(sk.Name)
	for _, w := range nameWords {
		if strings.Contains(lowerUserText, w) {
			score += scoreNameHit
		}
	}
	for _, w := range deriveWords(sk) {
		if strings.Contains(lowerUserText, w) {
			score += scoreWordHit
		}
	}
	for _, tok := range asciiWordRe.FindAllString(lowerUserText, -1) {
		for _, w := range nameWords {
			if len(tok) < len(w) && strings.HasPrefix(w, tok) {
				score += scoreNameHit
				break
			}
		}
	}
	return score
}
