package tools

import (
	"fmt"
	"strings"

	"localai/internal/codeindex"
)

func init() {
	register(&Tool{
		Schema: `{
  "type": "function",
  "function": {
    "name": "index_search",
    "description": "语义检索整个代码库：按相关度返回最相关的代码片段（含文件和行号）。回答「XX在哪实现的/怎么用的」这类问题时优先用它，比逐个读文件快且省上下文。首次调用会自动建索引。",
    "parameters": {
      "type": "object",
      "properties": {
        "query": {"type": "string", "description": "检索内容：功能描述、函数名、类名等"},
        "top_k": {"type": "integer", "description": "返回片段数，默认 5"}
      },
      "required": ["query"]
    }
  }
}`,
		ReadOnly: true,
		Exec:     execIndexSearch,
	})
}

func execIndexSearch(args map[string]any) string {
	query := strOf(args["query"])
	topK := 5
	if v, ok := args["top_k"].(float64); ok && int(v) > 0 {
		topK = int(v)
	}
	ws := GetWorkspace()
	codeindex.Ensure(ws, nil)
	hits := codeindex.Search(ws, query, topK)
	if len(hits) == 0 {
		return "未检索到相关代码（索引可能为空，或换个关键词试试）"
	}
	var out []string
	for _, h := range hits {
		out = append(out, fmt.Sprintf("### %s:%d-%d（相关度 %.4f）\n%s",
			h.File, h.StartLine, h.EndLine, h.Score, h.Content))
	}
	return strings.Join(out, "\n\n")
}
