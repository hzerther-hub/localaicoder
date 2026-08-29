# Repository Guidelines

## Project Overview

Local AI Studio — a local-first AI coding assistant: one pure-Go kernel (`internal/**`, module `localai`) consumed by three frontends: a CLI (`cmd/localai`), a Bubbletea TUI (`internal/tui`), and a Wails desktop app (`desktop/`, separate Go module + React/TS frontend). Streaming function-calling agent loop, readonly/ask/always permission modes, local→cloud model fallback, TF-IDF workspace RAG, MCP support. Comments and user-facing strings are in **Chinese**; the code is a documented 1:1 Go port of a Python kernel — wire formats and SQLite schemas are kept byte-compatible.

## Architecture & Data Flow

One kernel, three frontends. All frontends share `internal/config` state and the `internal/sessions` SQLite DB. Desktop is pure binding/bridging over `internal/*` — never reimplements kernel logic.

**Per-turn data flow** (`(*agent.Agent).Run` in `internal/agent/agent.go`):

1. Attachments → multimodal content parts (`internal/attach`, `internal/media` images become `image_url` data URLs); optional KB auto-inject via `codera.RetrieveContext` (`internal/agent/kbhook.go`).
2. Loop ≤ `config.MaxToolRounds` (12): `OnStop` check → `ctxcompact.MaybeCompact` (budget enforcement) → LLM cache replay (`cache.GetLLM`) → `llm.StreamChat` (SSE) emitting `text`/`reasoning`/`tool_calls`/`usage` events.
3. On `gpulocal*` model 503/loading errors + auto-fallback enabled: swap to `dispatch_flash` → `dispatch_pro` (cloud), emit `model_switch`, retry once.
4. Tool calls: ask-mode write tools go through `OnApproval`; execution via `tools.ExecuteTool` (`mcp_*` → MCP Manager, read-only → tool cache, write → direct); results appended as `role:"tool"` messages. Tool failures are **strings fed back to the model** — the loop never breaks on tool errors.
5. No tool calls → final text, response cached under request-time messages.

**Key modules**: `internal/msg` (wire contract: `Msg`/`Event` = `map[string]any` + accessors `S/B/F/I/M/L` — no structs, do not introduce them), `internal/llm` (shim-style protocol adapter layer: transports registry keyed by `model.APIFormat` — `chat_completions` / `anthropic_messages` / `responses` / `gemini`; every transport speaks the same internal event contract `text`/`reasoning`/`tool_calls`/`finish`/`usage`; `keypool.go` per-provider credential pool with round-robin rotation, 401/403 eviction and 402/429 30s cooldown — `StreamChat` retries transparently across keys; `ErrStop` cooperative stop), `internal/tools` (registry pattern: one file per tool, `init()`-self-registered `Tool` structs — fail-closed `ReadOnly=false`, optional `Enabled()` conditional exposure replaced the old separate schema maps; `registry.go` is the single source of truth; `sandbox.go` path/shell guards), `internal/config` (all settings in `models.json`, incl. `api_format`/`api_keys`/`auth_header`/`prompt_addendum` provider fields and the `smart_routing` block), `internal/routing` (smart routing: pure heuristic classifier + optional local-brain arbitration for borderline turns, one classification per user turn pinned for the whole turn), `internal/prompt` (sectioned system prompt with `SYSTEM_PROMPT_DYNAMIC_BOUNDARY` static/dynamic cache boundary and a minimal mode for gpulocal/≤32K models), `internal/mcp` (stdio + streamable HTTP, protocol 2024-11-05), `internal/cache` / `internal/sessions` / `internal/codeindex` / `internal/codera` (pure-Go SQLite), `internal/products` (white-label feature flags from `products/*/profile.json`).

**Tools**: 9 built-ins (one file each under `internal/tools/`, self-registered via `init()`; `registry_test.go` golden-locks the schema output): `read_file`, `write_file`, `list_dir`, `glob_search`, `grep_search`, `lsp_diagnostics`, `index_search`, `run_shell`, `web_search`; conditional `call_model` (dispatch: `Enabled()` = dispatch on + local brain healthy) and `kb_search` (RAG feature); MCP tools named `mcp_<server>_<tool>`. Write set = `write_file` + `run_shell` (plus non-`readonly` MCP servers). Tool schemas are sorted by name every round for byte-stable request prefixes (server prompt-cache optimization) — preserve `sortToolSchemas`. Adding a tool = new file with `register(&Tool{...})` (schema JSON verbatim, `Exec`, optional `ReadOnly`/`Enabled`/`Describe`).

**Smart routing** (`internal/routing` + `smart_routing` block in models.json): per user turn, `agent.routeTurn` classifies (turn 1 / code fences / strong keywords zh+en / multi-paragraph / length → strong; borderline weak-signal zone → optional `dispatch_model` arbitration, result hash-cached) and pins the model for the whole turn; `routing` events (`decision`: simple/strong/escalate) flow to UIs; simple-model retryable errors (404/429/5xx/conn) escalate to strong once per turn. Disabled unless `smart_routing.enabled` is explicitly set (legacy `dispatch_smart` flag intentionally does NOT enable it).

## Key Directories

- `cmd/localai/` — CLI entry (`chat`, `tui`, `models`, `sessions`, `version`, `help`); hand-rolled `os.Args` dispatch, no flag framework.
- `internal/` — the kernel; one package per concern, mostly one file per package (`tools/`, `llm/`, `config/`, `mcp/` are multi-file).
- `desktop/` — **separate Go module** (`localai/desktop`, `replace localai => ../`): `main.go` (Wails bootstrap, `go:embed frontend/dist` + `models.json`), `app.go` (~50 Wails-bound methods), `runner.go` (`RunManager`: one agent run at a time, events via `agent:event`, approvals via `approval:request` + `RespondApproval`).
- `desktop/frontend/` — React 18 + TypeScript + Vite + zustand; `bridge.ts` wraps Wails bindings, `store.ts` is the zustand store, `components/` holds panels.
- `products/` — product profiles: `profile.json` = `{title, features, exe_name}`; selected by `LOCAL_AI_PRODUCT` env, default `devtool_local`. Feature keys (e.g. `gpulocal`, `rag`, `zh_only`, `quant`) gate tool schemas and UI.

## Development Commands

```bash
# Kernel + CLI/TUI (pure Go, no CGO)
go build ./...            # compile everything (root module only)
go test ./...             # all tests (= make test)
go vet ./...              # the only linter configured
go run ./cmd/localai chat # streaming CLI REPL
go build -o bin/localai ./cmd/localai

# Desktop (separate module; needs CGO + WebView2/WebKit; production tag MANDATORY)
cd desktop/frontend && pnpm install && pnpm build   # tsc --noEmit && vite build
cd desktop && go build -tags desktop,production -ldflags "-s -w" -o bin/LocalAIStudio.exe .
cd desktop && wails dev     # hot-reload (needs: go install github.com/wailsapp/wails/v2/cmd/wails@latest)

# Makefile shortcuts
make build | test | vet | run | frontend | build-desktop | dev-desktop | package-desktop | clean
```

Single test: `go test ./internal/agent/ -run TestAgentToolLoop`.

## Code Conventions & Common Patterns

- **Language**: doc comments, error strings, and UI copy in Chinese (e.g. `"高危命令未被拦截"`); tool results start with `错误：` on failure.
- **Wire-format-first**: messages/events stay `map[string]any` (`msg.Msg`/`msg.Event`) matching OpenAI JSON 1:1; use `msg.S/B/F/I/M/L` accessors, never re-model into structs. Typed structs exist only at boundaries: `config.ModelConfig`, `sessions.Session`, `agent.Usage`.
- **Callback DI, not interfaces**: `agent.Agent` takes function fields `OnEvent` / `OnApproval` / `OnStop`; frontends plug in by constructing `&agent.Agent{...}`. Only notable interface: `mcp.transport`.
- **Package-global lazy state**: `sync.Once` singletons (`mcp.GetManager()`, `tools.ToolSchemas()`) and mutex-guarded package vars (workspace, DB handles). Every stateful package exposes a test-reset hook: `config.SetDir`, `cache.Reset`, `mcp.ResetManager`, `tools.PushWorkspace` (returns restore func), `codeindex.CloseAll`, `sessions.Close`, `products.ResetForTest`.
- **Error handling split**: transport errors are typed (`llm.LLMError`, `mcp.MCPError`) and abort the run; cooperative stop is the `llm.ErrStop` sentinel checked via `errors.Is`. Tool-layer failures are plain-text results; `ExecuteTool` recovers panics into text. Best-effort IO uses `_ = err`.
- **Concurrency**: goroutine + channel per long-running unit (MCP `readLoop` + per-request `pending` chans, TUI event channel, desktop run goroutine); `context.WithTimeout` for shell exec (`ToolExecTimeout` 60s); `signal.NotifyContext` in CLI. SQLite connections serialized (`SetMaxOpenConns(1)`, WAL).
- **Caching discipline**: sha256-of-JSON keys; LLM cache keyed on request-time messages (usage excluded); write tools and MCP calls never cached; deterministic marshalling required for any new cache key.
- **Port fidelity**: do not casually change event names, SQLite schemas, or tool schema JSON — they mirror the Python predecessor and legacy migration paths (`sessions/*.json`, legacy config dirs) depend on them.
- **Naming**: `exec<Tool>` unexported executors; `Get*/Set*` pairs in config; `*Schema()` functions for conditional tool schemas; constants like `ModeReadonly/ModeAsk/ModeAlways`.

## Important Files

- `cmd/localai/main.go` — CLI wiring: products → config → `tools.SetWorkspace` → MCP connect → `agent.Agent` construction → REPL.
- `internal/agent/agent.go` — the loop, permissions, cloud fallback, usage/cost accounting.
- `internal/llm/llm.go` — `StreamChat`, SSE parsing, tool-call delta accumulation, `max_tokens` policy (gpulocal capped 16384).
- `internal/tools/tools.go` (+ `dispatch.go`, `sandbox.go`, `websearch.go`) — registry, executors, sandbox, conditional tools.
- `internal/config/config.go` — config dir (`~/.config/local-ai-studio` / `%APPDATA%\local-ai-studio`, legacy migration), `models.json` CRUD, all getters/setters, `GetSystemPrompt()` (bilingual).
- `internal/msg/msg.go` — the kernel's data contract.
- `desktop/main.go`, `desktop/app.go`, `desktop/runner.go` — Wails bridge; frontend talks only to these bindings.
- `models.json` (repo root) — seed/example provider config: `{default, providers:[{id, base_url, api_key, models:[{id, vision, reasoning}]}]}`; model key = `provider_id/model_id`.
- `products/*/profile.json` — feature flags; `rag` gates `kb_search`, `zh_only` forces Chinese UI.

## Runtime/Tooling Preferences

- **Go 1.25** (both modules). Kernel deps deliberately minimal: `modernc.org/sqlite` (pure-Go — keep the CLI CGO-free/static) + charmbracelet TUI libs; everything else stdlib. Do not add SDK/HTTP-client deps; hand-rolled SSE/MCP/LSP clients are a project choice.
- **pnpm 10 exclusively** for the frontend (Node 20); never npm/yarn. Vite 6 + TS 5.6 strict mode; `pnpm build` includes typecheck (`build:fast` skips it). No ESLint/Prettier configured.
- Desktop builds require the `desktop,production` build tags and CGO; root-module commands never touch `desktop/` (separate module).
- Env vars the kernel reads: `LAS_SANDBOX=off` (disables write/shell sandbox), `LAS_PRODUCTS_DIR`, `LOCAL_AI_PRODUCT`. API keys live in `models.json` per provider (local providers use literal `"local-noauth"`), never env vars.
- CI (`.github/workflows/ci.yml`, push/PR to `master`): `go build/vet/test ./...` + frontend `pnpm install && pnpm build`. Desktop Go module and wails build are **not** covered by CI.

## Testing & QA

- **Stdlib `testing` only** — no testify/gomock; manual `if` + `t.Fatal(t.Errorf)`. No table-driven tests or `t.Run` subtests in the existing suite; follow the established per-`Test`-function style.
- Tests are **white-box** (same package) and integration-flavored through public APIs (`ExecuteTool`, `StreamChat`, `MaybeCompact`), asserting observable contracts (event sequences, schema counts, fallback behavior).
- **Isolation pattern** (copy it for new tests): package-local `setup(t)` helper → `t.TempDir()` + `config.SetDir(dir)` → seed fixtures with `os.WriteFile` → `t.Cleanup` calling the package's reset hook + `config.SetDir("")`.
- Fake LLM backends: `net/http/httptest` SSE servers (see `sseHandler`/`sse`/`toolCallJSON` helpers in `internal/agent/agent_test.go`); assert events by scanning `Event["type"]` via `msg.S`. No real network, no sleeps, no `testdata/` dirs.
- No coverage threshold, no `-race` in CI or Makefile. Tests create real SQLite DBs in temp dirs — keep them deterministic.
- Untested packages (`msg`, `mcp`, `lsp`, `tui`, `attach`, `media`, `embed`, `localmodels`, `products`, `codera`, `cmd/`, all of `desktop/`) — when adding tests there, follow the `internal/agent` pattern.
