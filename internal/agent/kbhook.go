package agent

import (
	"localai/internal/codera"
	"localai/internal/config"
)

// coderaRetrieve 知识库自动注入检索（kbRetrieve 的实际实现，隔离 recover 边界）。
func coderaRetrieve(query string, topK int) string {
	roots := config.GetKBRoots()
	codera.MaybeAutoRefresh(roots)
	return codera.RetrieveContext(query, topK, 4000, roots)
}
