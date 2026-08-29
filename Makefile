# Local AI Studio — Go 内核 + CLI + TUI + Wails 桌面
# Windows 下用 mingw32-make / make（Git Bash 自带）

GO      ?= go
VERSION ?= 0.1.0

## ---------- 内核 / CLI / TUI（纯 Go，无 CGO） ----------

.PHONY: build
build: ## 编译 CLI（含 tui 子命令）
	$(GO) build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/localai.exe ./cmd/localai

.PHONY: build-all
build-all: build build-desktop ## 全部产物

.PHONY: test
test: ## 全部 Go 测试
	$(GO) test ./...

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: run
run: ## CLI 流式 REPL
	$(GO) run ./cmd/localai chat

## ---------- 桌面（Wails，需 CGO + WebView2） ----------

# Linux 按发行版选择 webkit 标签（本机装的是 webkit2gtk-4.1 → webkit2_41）；
# Windows/WebView2 不需要该标签。
ifeq ($(shell uname 2>/dev/null),Linux)
DESKTOP_TAGS  := desktop,production,webkit2_41
DESKTOP_OUT   := bin/LocalAIStudio
else
DESKTOP_TAGS  := desktop,production
DESKTOP_OUT   := bin/LocalAIStudio.exe
endif

.PHONY: frontend
frontend: ## 构建前端产物（desktop/frontend/dist）
	cd desktop/frontend && pnpm install && pnpm build

.PHONY: build-desktop
build-desktop: frontend ## 编译桌面应用（必须带 production 标签！Linux 自动加 webkit2_41）
	cd desktop && $(GO) build -tags $(DESKTOP_TAGS) -ldflags "-s -w" -o $(DESKTOP_OUT) .

.PHONY: dev-desktop
dev-desktop: ## 桌面开发模式（热重载；需 wails CLI: go install github.com/wailsapp/wails/v2/cmd/wails@latest）
	cd desktop && wails dev

.PHONY: package-desktop
package-desktop: ## 正式打包（NSIS/dmg 产物）
	cd desktop && wails build -clean

## ---------- 杂项 ----------

.PHONY: clean
clean:
	rm -rf bin desktop/bin

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "} {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
