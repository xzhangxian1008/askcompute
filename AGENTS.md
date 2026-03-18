# AGENTS.md

Onboarding guide for AI agents working on this codebase.

## What This Project Does

askplanner_v2 is a lightweight Go relay layer for **TiDB SQL query tuning**. It receives user questions (via CLI or Lark bot), forwards them to [Codex CLI](https://github.com/openai/codex), and returns the answer. All agent reasoning happens inside Codex CLI — this project handles session management, prompt loading, and I/O.

## Architecture

```
User Question
     |
     v
+-----------------+     +-----------------+
| cmd/askplanner  |     | cmd/larkbot     |   <-- frontends
| (CLI REPL)      |     | (Feishu bot)    |
+--------+--------+     +--------+--------+
         |                        |
         v                        v
   +-----+--------------------------+
   |    internal/codex/responder    |   <-- session mgmt, orchestration
   +-----+--------------------------+
         |
         v
   +-----+--------------------------+
   |    internal/codex/runner       |   <-- exec `codex` CLI process
   +--------------------------------+
         |
         v
   Codex CLI (external binary)
         |
         v
   Answer (via reply file or stdout JSON)
```

## Key Files

| File | Role |
|---|---|
| `prompt` | Pre-assembled 18KB system prompt. Defines the TiDB tuning agent persona, rules, tool adaptation, and skill references. Codex CLI receives this as its instruction. |
| `internal/config/config.go` | Config loading from env vars. Also provides `SetupLogging()`. |
| `internal/codex/responder.go` | Core orchestration: decides resume vs new session, calls runner, stores results. Entry point: `NewResponder(cfg)` then `Answer(ctx, key, question)`. |
| `internal/codex/runner.go` | Wraps `codex exec` CLI invocation. Handles `RunNew()` and `RunResume()`, parses JSON stdout for thread IDs and answers. |
| `internal/codex/session_store.go` | Thread-safe JSON file store for session records. Tracks conversation turns, prompt hash, TTL. |
| `internal/codex/prompt.go` | `LoadPrompt()` reads the prompt file. `PromptHash()` for versioning. `BuildInitialPrompt()` / `BuildResumePrompt()` construct what gets sent to Codex. |
| `cmd/askplanner/main.go` | CLI REPL. Commands: type a question, `reset`, `quit`. |
| `cmd/larkbot/main.go` | Feishu/Lark bot via WebSocket. Derives conversation keys from thread/chat/user IDs. |

## contrib/ Submodules

| Submodule | Purpose |
|---|---|
| `contrib/agent-rules` | Curated skills library (from `pingcap/agent-rules`). The `tidb-query-tuning` skill contains oncall patterns, diagnostic workflows, and reference docs. |
| `contrib/tidb` | TiDB source code (from `pingcap/tidb`). Ground truth for optimizer internals. |
| `contrib/tidb-docs` | TiDB official documentation (from `pingcap/docs`). Referenced by the prompt for SQL syntax, hints, and best practices. |

Codex CLI runs with `WorkDir` set to the project root, so it can access all `contrib/` resources via shell commands.

## Build

```bash
make all        # builds bin/askplanner_cli and bin/askplanner_larkbot
make cli        # builds bin/askplanner_cli only
make larkbot    # builds bin/askplanner_larkbot only
make clean      # removes binaries
make fmt        # go fmt
```

Requires: Go 1.23+, `codex` CLI in PATH.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PROJECT_ROOT` | auto-detected (walks up looking for `prompt` file) | Project root directory |
| `PROMPT_FILE` | `prompt` | Path to the system prompt file |
| `CODEX_BIN` | `codex` | Path to codex CLI binary |
| `CODEX_MODEL` | `gpt-5.3-codex` | Model identifier |
| `CODEX_REASONING_EFFORT` | `medium` | Reasoning effort level |
| `CODEX_SANDBOX` | `read-only` | Sandbox mode for codex |
| `CODEX_SESSION_STORE` | `.askplanner/sessions.json` | Session persistence file |
| `CODEX_MAX_TURNS` | `30` | Max turns before session reset |
| `CODEX_SESSION_TTL_HOURS` | `24` | Session TTL in hours |
| `CODEX_TIMEOUT_SEC` | `120` | Per-call timeout |
| `LOG_FILE` | `.askplanner/askplanner.log` | Log file path (also logs to stderr) |
| `FEISHU_APP_ID` | — | Required for larkbot |
| `FEISHU_APP_SECRET` | — | Required for larkbot |

## Session Management

- Sessions are keyed by conversation ID (`cli:default` for CLI, `lark:thread:*` / `lark:chat:*:user:*` for bot).
- A session can be **resumed** if: same prompt hash, same work dir, turn count < max, TTL not expired.
- If resume fails, a **new session** starts automatically with a summary of the last 6 turns as context.
- Changing the `prompt` file invalidates all sessions (prompt hash changes).

## How Codex CLI Is Invoked

New session:
```
codex exec --sandbox read-only --json --model <model> -c model_reasoning_effort="medium" -o <reply_file> - < <prompt>
```

Resume session:
```
codex exec resume --json --model <model> -c model_reasoning_effort="medium" -o <reply_file> <session_id> - < <prompt>
```

The answer is read from the reply file (`-o`), or extracted from JSON stdout events as fallback.

## Code Conventions

- Module: `lab/askplanner`
- Standard library `log` package with file + stderr multi-writer
- No external dependencies beyond Lark SDK
- Config via environment variables only, no config files
- All paths resolved relative to project root
