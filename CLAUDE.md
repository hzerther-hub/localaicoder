# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> **Companion doc**: `AGENTS.md` in the repo root is the authoritative project guide (architecture, data flow, conventions, file map). It is thorough and written by/for this project. **Read `AGENTS.md` first for anything this file doesn't cover** — this file deliberately omits detail that lives there.

## Project overview

Local AI Studio — a local-first AI coding assistant. One pure-Go kernel (`internal/**`, module `localai`) consumed by three frontends: a CLI (`cmd/localai`), a Bubbletea TUI (`internal/tui`), and a Wails desktop app (`desktop/`, a **separate Go module** `localai/desktop` + React/TS frontend). Core is a streaming function-calling agent loop with read/ask/always permission tri-states, local→cloud model fallback, and a shim protocol layer (chat_completions / anthropic_messages / responses / gemini) that translates one internal message language into different API-formats.

Comments and user-visible strings are written in **Chinese**.

## Build / test / lint commands

There are two Go modules: the root module (pure Go, no CGO) and `desktop/` (separate module).

### Kernel + CLI/TUI (root module)

```bash
go build ./...                                   # build everything (root module only)
go test ./...                                    # all tests  (== `make test`)
go vet ./...                                     # the only configured lint
go build -o bin/localai ./cmd/localai
go run ./cmd/localai chat                        # streaming CLI REPL

# single test
go test ./internal/agent/ -run TestAgentToolLoop
```

### Wails desktop (separate module; needs CGO + WebView2/WebKit)

**The `desktop,production` build tags are mandatory** (production enables the embedded frontend; on Linux add `webkit2_41` for webkit2gtk-4.1). Root-module commands never touch `desktop/`.

```bash
cd desktop/frontend && pnpm install && pnpm build   # tsc --noEmit && vite build
cd desktop && go build -tags desktop,production,webkit2_41 -ldflags "-s -w" -o bin/LocalAIStudio .
cd desktop && wails dev                              # hot reload
```

`make ...` shorthand targets: `build` (CLI), `test`, `vet`, `run`, `frontend`, `build-desktop`, `dev-desktop`, `package-desktop`, `clean`.

### relay-server (Python, separate)

```bash
cd relay-server && pip install -r requirements.txt && python main.py
```

## Architecture (big picture)

A single kernel, three frontends. All frontends share `internal/config` state and the `internal/sessions` SQLite store. The desktop is a thin binding/bridge layer over `internal/*` — it never reimplements kernel logic.

**Per-turn data flow** (`(*agent.Agent).Run`, `internal/agent/agent.go`):
1. Attachments → multimodal content parts (`internal/attach`, `internal/media`); optional KB injection (`codera.RetrieveContext`).
2. Loop ≤ `config.MaxToolRounds` (12): `OnStop` check → `ctxcompact.MaybeCompact` → LLM cache replay → `llm.StreamChat` (SSE) emitting `text`/`reasoning`/`tool_calls`/`usage` events.
3. Hit `gpulocal*` 503/load error with auto-fallback on → switch to `dispatch_flash` → `dispatch_pro`, emit `model_switch`, retry once.
4. Tool call: ask-mode write tools go through `OnApproval` first; execute via `tools.ExecuteTool`; failure is fed back **as a string** to the model — the loop never aborts on tool errors.
5. No tool call → emit final text, cache the response keyed by request message.

**Modules worth knowing** (each is a package with a single concern, mostly one file):
- `internal/msg` — wire contract: `Msg`/`Event` = `map[string]any` + accessors `S/B/F/I/M/L`. **Do not introduce structs here.**
- `internal/llm` — shim protocol layer; `keypool.go` is the provider credential pool; `ErrStop` is a cooperative-stop sentinel (`errors.Is`).
- `internal/tools` — registry pattern: one file per tool, `init()` self-registers a `Tool` struct. `registry.go` is the single source of truth; `sandbox.go` does path/shell protection.
- `internal/config` — everything lives in `models.json` (`api_format`/`api_keys`/`auth_header`/`prompt_addendum` provider fields, `smart_routing` block).
- `internal/routing` — heuristic router (simple vs strong model), fixed for the whole turn.
- `internal/prompt` — partitioned system prompt with a static/dynamic cache boundary (`SYSTEM_PROMPT_DYNAMIC_BOUNDARY`).

**Tools** (11 built-in + conditional): `read_file`, `write_file`, `list_dir`, `glob_search`, `grep_search`, `lsp_diagnostics`, `index_search`, `run_shell`, `web_search`, `todo_write`, plus conditional `call_model` and `kb_search`; MCP tools named `mcp_<server>_<tool>`. Tool schemas are sorted by name each turn for byte-stable request prefixes (`sortToolSchemas`) — **preserve this** for prompt-cache prefix stability.

## Key conventions

- **Wire-format-first**: messages/events stay `map[string]any` (`msg.Msg`/`msg.Event`), a 1:1 analogue of OpenAI JSON, accessed via `msg.S/B/F/I/M/L`. Typed structs only at boundaries (`config.ModelConfig`, `sessions.Session`, `agent.Usage`).
- **Callback DI, not interfaces**: `agent.Agent` takes function fields `OnEvent`/`OnApproval`/`OnStop`; frontends wire in via `&agent.Agent{...}`. The one notable interface is `mcp.transport`.
- **Package-level lazy state**: `sync.Once` singletons and mutex-guarded package vars. Test state must expose a reset hook: `config.SetDir`, `cache.Reset`, `mcp.ResetManager`, `tools.PushWorkspace` (returns restore func), `codeindex.CloseAll`, `sessions.Close`, `products.ResetForTest`.
- **Error handling split**: typed transport errors (`llm.LLMError`, `mcp.MCPError`) interrupt a turn; `llm.ErrStop` is cooperative. Tool-layer failures are plain-text results; `ExecuteTool` recovers panics to text. Best-effort IO uses `_ = err`.
- **Concurrency**: each long-running unit gets a goroutine + channel. Shell uses `context.WithTimeout` (`ToolExecTimeout` 60s). SQLite is serialized (`SetMaxOpenConns(1)`, WAL).
- **Cache discipline**: sha256-of-JSON keys; LLM cache keyed by request-time messages (excluding usage); write tools and MCP calls are never cached. New cache keys need deterministic marshalling.
- **Naming**: `exec<Tool>` non-exported executors; config uses `Get*/Set*` pairs; conditional schemas via `*Schema()` functions; constants like `ModeReadonly/ModeAsk/ModeAlways`.

## Testing & QA

- **stdlib `testing` only** — no testify/gomock; manual `if` + `t.Fatal`/`t.Errorf`. No table-driven or `t.Run` subtests; one `Test` function per concern.
- Tests are **white-box** (same package), integration-style through public APIs (`ExecuteTool`, `StreamChat`, `MaybeCompact`), asserting observable contracts (event sequences, schema counts, fallback behavior).
- **Isolation pattern** (copy for new tests): a package `setup(t)` helper → `t.TempDir()` + `config.SetDir(dir)` → seed fixtures with `os.WriteFile` → `t.Cleanup` calls the reset hook + `config.SetDir("")`.
- Fake LLM backend: `net/http/httptest` SSE server (see `internal/agent/agent_test.go` helpers `sseHandler`/`sse`/`toolCallJSON`). Scan events with `msg.S` on `Event["type"]`. No real network, no `sleep`, no `testdata/` dirs.

## Runtime & tool preferences

- **Go 1.25** on both modules. Kernel deps are deliberately minimal: `modernc.org/sqlite` (pure Go — keeps CLI CGO-free/static) + charmbracelet TUI libs; everything else stdlib. Don't add new SDK/HTTP-client deps — hand-written SSE/MCP/LSP clients are a project choice.
- **pnpm 10 exclusively** for the frontend (Node 20); never npm/yarn. Vite 6 + TS 5.6 strict. `pnpm build` includes type-checking (`build:fast` skips it). No ESLint/Prettier config.
- Desktop builds require `desktop,production` tags + CGO; Linux additionally `webkit2_41`.
- Kernel env vars: `LAS_SANDBOX=off` (disable write/shell sandbox), `LAS_PRODUCTS_DIR`, `LOCAL_AI_PRODUCT`. API keys live in `models.json` per-provider (local providers use literal `"local-noauth"`), never in env vars.
- CI (`.github/workflows/ci.yml`): `go build/vet/test ./...` + frontend `pnpm install && pnpm build`. Desktop Go module and wails builds are **not** covered by CI. Note: CI/AGENTS.md reference branch `master`, but the actual current default branch is `main`.

## Where things live

- `cmd/localai/main.go` — CLI wiring: products → config → `tools.SetWorkspace` → MCP connect → `agent.Agent` construct → REPL.
- `internal/agent/agent.go` — loop, permissions, cloud fallback, usage/cost accounting.
- `internal/llm/llm.go` — `StreamChat`, SSE parsing, tool-call delta accumulation, `max_tokens` policy (gpulocal cap 16384).
- `internal/tools/` (+ `dispatch.go`, `sandbox.go`, `websearch.go`, `todo.go`) — registry, executors, sandbox, conditional tools.
- `desktop/main.go`, `desktop/app.go`, `desktop/runner.go` — Wails bridge; the frontend only talks to these bindings.
- `models.json` (repo root) — seed/example provider config; model key = `provider_id/model_id`.
- `products/*/profile.json` — feature switches; `rag` gates `kb_search`, `zh_only` forces Chinese UI. Selected by `LOCAL_AI_PRODUCT`, default `devtool_local`.
