package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func init() {
	register(&Tool{
		Schema: `{
  "type": "function",
  "function": {
    "name": "glob_search",
    "description": "按通配符查找文件，如 '*.py' 或 '**/*.js'。",
    "parameters": {
      "type": "object",
      "properties": {
        "pattern": {"type": "string", "description": "glob 通配符模式"}
      },
      "required": ["pattern"]
    }
  }
}`,
		ReadOnly: true,
		Exec:     execGlobSearch,
	})
}

func execGlobSearch(args map[string]any) string {
	pattern := strOf(args["pattern"])
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(GetWorkspace(), pattern)
	}
	matches := globPattern(pattern)
	if len(matches) == 0 {
		return "未找到匹配文件"
	}
	sort.Strings(matches)
	if len(matches) > 200 {
		matches = matches[:200]
	}
	return strings.Join(matches, "\n")
}

// globPattern 支持 ** 的 glob（filepath.Glob 不支持 **，手工实现）。
func globPattern(pattern string) []string {
	if !strings.Contains(pattern, "**") {
		m, _ := filepath.Glob(pattern)
		return m
	}
	// 形如 <root>/**/rest 或 <root>/**；把 ** 段展开为任意深度
	idx := strings.Index(pattern, "**")
	root := filepath.Dir(pattern[:idx+1]) // ** 所在目录
	rest := ""
	if idx+2 < len(pattern) {
		rest = filepath.ToSlash(strings.TrimPrefix(pattern[idx+2:], string(filepath.Separator)))
	}
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rest == "" || rest == "*" {
			if info.IsDir() || rest == "*" {
				out = append(out, p)
			}
			return nil
		}
		if matchGlobRest(rel, rest) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// matchGlobRest 逐段匹配（rest 各段可含 *；开头的 ** 段匹配任意数量目录，
// 其余 ** 段等价任意深度；模式无 ** 时必须整体匹配）。
func matchGlobRest(rel, rest string) bool {
	if rel == "." {
		return false
	}
	rSegs := strings.Split(rel, "/")
	mSegs := strings.Split(rest, "/")
	var match func(r, m []string) bool
	match = func(r, m []string) bool {
		if len(m) == 0 {
			return len(r) == 0
		}
		if m[0] == "**" {
			for i := 0; i <= len(r); i++ {
				if match(r[i:], m[1:]) {
					return true
				}
			}
			return false
		}
		if len(r) == 0 {
			return false
		}
		if ok, _ := filepath.Match(m[0], r[0]); !ok {
			return false
		}
		return match(r[1:], m[1:])
	}
	// 模式以 ** 开头（外层已剥离）→ rest 匹配 rel 的任意尾部（含整体）
	if len(rSegs) >= len(mSegs) && match(rSegs[len(rSegs)-len(mSegs):], mSegs) {
		return true
	}
	return match(rSegs, mSegs)
}
