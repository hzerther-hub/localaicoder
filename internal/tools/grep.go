package tools

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func init() {
	register(&Tool{
		Schema: `{
  "type": "function",
  "function": {
    "name": "grep_search",
    "description": "在文件内容中按正则/关键字搜索。",
    "parameters": {
      "type": "object",
      "properties": {
        "pattern": {"type": "string", "description": "要搜索的关键字或正则"},
        "path": {"type": "string", "description": "目录或文件路径，默认工作目录"}
      },
      "required": ["pattern", "path"]
    }
  }
}`,
		ReadOnly: true,
		Exec:     execGrepSearch,
	})
}

// grep 跳过超过该大小的文件（多为二进制/生成物）
const maxGrepFile = 5 * 1024 * 1024

var grepSkipDirs = map[string]bool{
	"node_modules": true, "__pycache__": true, "dist": true, "build": true,
}

func execGrepSearch(args map[string]any) string {
	pattern := strOf(args["pattern"])
	root := GetWorkspace()
	if v := strOf(args["path"]); v != "" {
		root = resolve(v)
	}
	var targets []string
	if st, err := os.Stat(root); err == nil && !st.IsDir() {
		targets = []string{root}
	} else {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				name := info.Name()
				if strings.HasPrefix(name, ".") || grepSkipDirs[name] {
					return filepath.SkipDir
				}
				return nil
			}
			targets = append(targets, p)
			return nil
		})
	}
	rx, err := regexp.Compile(pattern)
	if err != nil {
		rx = regexp.MustCompile(regexp.QuoteMeta(pattern)) // 无效正则 → 字面量
	}
	var hits []string
	for _, fpath := range targets {
		st, err := os.Stat(fpath)
		if err != nil || st.Size() > maxGrepFile {
			continue
		}
		f, err := os.Open(fpath)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		i := 0
		for scanner.Scan() {
			i++
			line := scanner.Text()
			if rx.MatchString(line) {
				t := strings.TrimSpace(line)
				if len(t) > 160 {
					t = t[:160]
				}
				hits = append(hits, fmt.Sprintf("%s:%d: %s", fpath, i, t))
				if len(hits) >= 100 {
					_ = f.Close()
					return strings.Join(hits, "\n") + "\n(结果已截断)"
				}
			}
		}
		_ = f.Close()
	}
	if len(hits) == 0 {
		return "未找到匹配内容"
	}
	return strings.Join(hits, "\n")
}
