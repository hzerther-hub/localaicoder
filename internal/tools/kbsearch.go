package tools

import (
	"fmt"
	"strings"

	"localai/internal/codera"
	"localai/internal/config"
	"localai/internal/products"
)

func init() {
	register(&Tool{
		Schema: `{
  "type": "function",
  "function": {
    "name": "kb_search",
    "description": "检索公司知识库（企业代码 + 文档）：按相关度返回最相关的代码/文档片段（含知识根目录、文件与行号）。回答涉及公司内部代码/文档、跨仓库问题时优先用它，比逐个读文件快且省上下文。首次调用会自动建索引；与 index_search（当前工作目录）不同，kb_search 覆盖配置的多个公司根目录。",
    "parameters": {
      "type": "object",
      "properties": {
        "query": {"type": "string", "description": "检索内容：功能描述、函数名、类名、文档关键词等"},
        "top_k": {"type": "integer", "description": "返回片段数，默认取配置（4）"}
      },
      "required": ["query"]
    }
  }
}`,
		ReadOnly: true,
		// 条件暴露：rag 产品功能 + 知识库开关 + 已配置知识根目录
		Enabled: func() bool {
			return products.Feature("rag", false) &&
				config.GetKBEnabled() && len(config.GetKBRoots()) > 0
		},
		Exec: execKBSearch,
	})
}

func execKBSearch(args map[string]any) string {
	roots := config.GetKBRoots()
	if len(roots) == 0 {
		return "错误：公司知识库尚未配置知识根目录。请在「📚 知识库」面板添加代码/文档目录后再检索。"
	}
	query := strings.TrimSpace(strOf(args["query"]))
	if query == "" {
		return "错误：query 不能为空"
	}
	topK := 0
	if v, ok := args["top_k"].(float64); ok && int(v) > 0 {
		topK = int(v)
	}
	if topK == 0 {
		topK = config.GetKBTopK()
	}
	codera.MaybeAutoRefresh(roots)
	hits := codera.Search(query, topK, roots)
	if len(hits) == 0 {
		return "未在知识库检索到相关内容（索引可能为空，或换个关键词；可在「📚 知识库」面板重建索引后重试）"
	}
	var out []string
	for _, h := range hits {
		out = append(out, fmt.Sprintf("### [%s 相关度 %.4f] %s/%s:%d-%d\n%s",
			h.Source, h.Score, h.Root, h.File, h.StartLine, h.EndLine, h.Content))
	}
	return strings.Join(out, "\n\n")
}
