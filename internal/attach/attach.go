// Package attach 附件分析：文本内联 / docx 提取 / 压缩包清单 / 代码片段格式化
//（对译 Python attach.py 的核心路径；PDF 在 Go 版暂以路径说明代替内联提取）。
package attach

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"localai/internal/config"
)

const (
	maxInlineChars = 20000
	maxListEntries = 50
)

var textExts = map[string]bool{
	".txt": true, ".md": true, ".csv": true, ".log": true, ".json": true,
	".xml": true, ".htm": true, ".html": true,
}

var archiveExts = map[string]bool{
	".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true,
}

var shellOnlyExts = map[string]bool{".rar": true, ".7z": true}

// Analyze 就地分析附件：文本类直接内联，docx 提取正文，压缩包解压列清单；
// 不认识的类型返回空（上层记为普通附件路径）。
func Analyze(path string) string {
	name := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case textExts[ext]:
		return inlineText(path, name)
	case ext == ".docx":
		return docxText(path, name)
	case ext == ".pdf":
		// Go 版暂不内联 PDF 正文：给模型路径说明，可用 run_shell 配工具提取
		return fmt.Sprintf("[PDF 附件: %s（%s）] 可用 run_shell 调用 pdf 文本工具提取后分析。",
			path, name)
	case archiveExts[ext]:
		return extractArchive(path, name)
	case shellOnlyExts[ext]:
		return fmt.Sprintf("[压缩包附件: %s]（%s 格式需 run_shell 调 7z/unrar 处理）", path, ext)
	case ext == ".py" || ext == ".js" || ext == ".ts" || ext == ".go" || ext == ".java" || ext == ".c" || ext == ".cpp":
		return inlineText(path, name)
	}
	return ""
}

func readText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func inlineText(path, name string) string {
	t := readText(path)
	if t == "" {
		return ""
	}
	if len(t) > maxInlineChars {
		t = t[:maxInlineChars] + "\n…[已截断]"
	}
	return fmt.Sprintf("📎 附件文件 %s 内容：\n```\n%s\n```", name, t)
}

// docxText 从 docx（zip + word/document.xml）提取纯文本。
func docxText(path, name string) string {
	z, err := zip.OpenReader(path)
	if err != nil {
		return ""
	}
	defer z.Close()
	var f *zip.File
	for _, file := range z.File {
		if file.Name == "word/document.xml" {
			f = file
			break
		}
	}
	if f == nil {
		return ""
	}
	rc, err := f.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 16<<20))
	if err != nil {
		return ""
	}
	text := xmlText(string(raw))
	if len(text) > maxInlineChars {
		text = text[:maxInlineChars] + "\n…[已截断]"
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return fmt.Sprintf("📎 附件文档 %s 正文：\n%s", name, text)
}

// xmlText 剥掉 XML 标签，段落分隔 <w:p>，空格归一。
func xmlText(s string) string {
	paraRe := regexp.MustCompile(`(?i)</w:p>`)
	s = paraRe.ReplaceAllString(s, "\n")
	tagRe := regexp.MustCompile(`<[^>]+>`)
	s = tagRe.ReplaceAllString(s, "")
	dec, _ := resolveEntities(s)
	return strings.TrimSpace(dec)
}

func resolveEntities(s string) (string, error) {
	out := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'",
	).Replace(s)
	return out, nil
}

func safeZipMembers(z *zip.Reader) []string {
	var out []string
	for _, f := range z.File {
		name := filepath.Clean(f.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue // 跳过路径穿越成员
		}
		out = append(out, f.Name)
		if len(out) >= maxListEntries {
			break
		}
	}
	return out
}

func extractDirFor(path string) string {
	// <extract>/<文件名>-<hash8>/ 子目录（与 Python 版一致的布局思想）
	h := hash8(path)
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return filepath.Join(config.ExtractDir(), fmt.Sprintf("%s-%s", name, h))
}

// extractArchive 解压 zip 到 extract 目录并返回清单说明。
func extractArchive(path, name string) string {
	if strings.ToLower(filepath.Ext(path)) != ".zip" {
		// tar/gz 等格式：只列说明（run_shell 可自行解压）
		return fmt.Sprintf("[压缩包附件: %s（%s）] 可用 run_shell 解压后分析。", path, name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	br := bytesReader(data)
	z, err := zip.NewReader(br, int64(len(data)))
	if err != nil {
		return ""
	}
	dir := extractDirFor(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	var listing []string
	for _, f := range z.File {
		clean := filepath.Clean(f.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		target := filepath.Join(dir, clean)
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			continue
		}
		_, _ = io.Copy(out, io.LimitReader(rc, 64<<20))
		out.Close()
		rc.Close()
		listing = append(listing, clean)
		if len(listing) >= maxListEntries {
			break
		}
	}
	if len(listing) == 0 {
		return ""
	}
	if len(listing) >= maxListEntries {
		listing = append(listing, "…（更多略）")
	}
	return fmt.Sprintf("📎 压缩包 %s 已解压到 %s，内容清单：\n%s",
		name, dir, strings.Join(listing, "\n"))
}

// FormatSnippet 代码片段附件（编辑器选区右键加入聊天）格式化为内联文本。
// att: {"kind":"snippet","path","start","end","text"}。
func FormatSnippet(att map[string]any) string {
	path := strOf(att["path"])
	text := strOf(att["text"])
	start := intOf(att["start"])
	end := intOf(att["end"])
	return fmt.Sprintf("📎 代码片段 %s（第 %d-%d 行）：\n```\n%s\n```",
		path, start, end, text)
}

// SnippetChip 附件条上的简短标签。
func SnippetChip(att map[string]any) string {
	name := filepath.Base(strOf(att["path"]))
	return fmt.Sprintf("%s:%d-%d", name, intOf(att["start"]), intOf(att["end"]))
}

// ExtractFileRefs 从消息文本里识别本地文件引用（file:// URI、绝对路径），
// 返回 (路径列表, 去引用后的文本)。对译 Python attach.extract_file_refs。
func ExtractFileRefs(text string) ([]string, string) {
	var paths []string
	fileURIRe := regexp.MustCompile("file://(/[^\\s，。；：！？、）」』\\]）〉>]+)")
	text = fileURIRe.ReplaceAllStringFunc(text, func(m string) string {
		p := strings.TrimPrefix(m, "file://")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			paths = append(paths, p)
			return ""
		}
		return m
	})
	winRe := regexp.MustCompile(`[A-Za-z]:[\\/](?:[\w .\-\u4e00-\u9fff]+[\\/])*[\w .\-\u4e00-\u9fff]+\.\w{1,8}`)
	posixRe := regexp.MustCompile(`(?:/[\w.\-\u4e00-\u9fff]+){2,}\.\w{1,8}`)
	collect := func(re *regexp.Regexp) {
		text = re.ReplaceAllStringFunc(text, func(m string) string {
			p := m
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				paths = append(paths, p)
				return ""
			}
			return m
		})
	}
	if isWindows() {
		collect(winRe)
	} else {
		collect(posixRe)
	}
	return paths, strings.TrimSpace(text)
}

func isWindows() bool {
	return filepath.Separator == '\\'
}

func strOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

func hash8(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}
