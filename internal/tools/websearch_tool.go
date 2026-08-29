package tools

func init() {
	register(&Tool{
		Schema: `{
  "type": "function",
  "function": {
    "name": "web_search",
    "description": "联网搜索（Bing/DuckDuckGo/百度自动回退）。返回结果列表（标题/链接/摘要）。查最新信息、找库/文档、排查报错时优先使用。",
    "parameters": {
      "type": "object",
      "properties": {
        "query": {"type": "string", "description": "搜索关键词"},
        "max_results": {"type": "integer", "description": "返回条数，默认 8，最大 10"}
      },
      "required": ["query"]
    }
  }
}`,
		ReadOnly: true,
		Exec:     execWebSearch,
	})
}
