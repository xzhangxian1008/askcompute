# askplanner_v2

Go relay for **TiDB SQL query tuning**. Receives questions (CLI or Lark bot) → forwards to [Codex CLI](https://github.com/openai/codex) → returns answer. This project handles session management, prompt loading, and I/O; all reasoning happens inside Codex CLI.

## Architecture

```
cmd/askplanner (CLI REPL)  ─┐
cmd/larkbot (Feishu bot)   ─┤
                             ├→ internal/codex/responder (session mgmt)
                             │       → internal/codex/runner (exec codex CLI)
                             │            → codex exec ... (external binary)
                             │                 → answer (reply file or JSON stdout)
```

## Key Files

| File | Role |
|---|---|
| `prompt` | 18KB system prompt: TiDB tuning persona, tool adaptation rules, skill refs |
| `internal/config/config.go` | Env-var config loading, `SetupLogging()` |
| `internal/codex/responder.go` | Orchestration: resume vs new session, calls runner. Entry: `NewResponder(cfg).Answer(ctx, key, question)` |
| `internal/codex/runner.go` | `RunNew()` / `RunResume()` — wraps `codex exec`, parses JSON stdout |
| `internal/codex/session_store.go` | Thread-safe JSON file store for sessions (turns, prompt hash, TTL) |
| `internal/codex/prompt.go` | `LoadPrompt()`, `PromptHash()`, `BuildInitialPrompt()` / `BuildResumePrompt()` |
| `cmd/askplanner/main.go` | CLI REPL (`reset`, `quit`) |
| `cmd/larkbot/main.go` | Feishu WebSocket bot, conversation key derivation, message dedup |

## contrib/ Submodules

| Submodule | Source | Purpose |
|---|---|---|
| `contrib/agent-rules` | `pingcap/agent-rules` | Skills library: oncall patterns, diagnostic workflows |
| `contrib/tidb` | `pingcap/tidb` | TiDB source — optimizer internals ground truth |
| `contrib/tidb-docs` | `pingcap/docs` | Official TiDB docs for SQL syntax, hints, best practices |

Codex CLI `WorkDir` = project root, so it reads `contrib/` via shell commands (`rg`, `cat`, etc.).

## Build & Run

```bash
make all          # bin/askplanner_cli + bin/askplanner_larkbot
make larkbot      # larkbot only
```

Requires: **Go 1.23+**, **codex CLI** in PATH, git submodules initialized.

## Environment Variables

| Variable | Default | Notes |
|---|---|---|
| `FEISHU_APP_ID` | — | **Required** for larkbot |
| `FEISHU_APP_SECRET` | — | **Required** for larkbot |
| `CODEX_BIN` | `codex` | Path to codex binary |
| `CODEX_MODEL` | `gpt-5.3-codex` | |
| `CODEX_REASONING_EFFORT` | `medium` | `low` / `medium` / `high` |
| `CODEX_SANDBOX` | `read-only` | Always read-only |
| `CODEX_SESSION_STORE` | `.askplanner/sessions.json` | |
| `CODEX_MAX_TURNS` | `30` | Turns before auto-reset |
| `CODEX_SESSION_TTL_HOURS` | `24` | |
| `CODEX_TIMEOUT_SEC` | `120` | Per-call timeout |
| `LOG_FILE` | `.askplanner/askplanner.log` | |
| `PROJECT_ROOT` | auto-detected | Walks up looking for `prompt` file |
| `PROMPT_FILE` | `prompt` | Relative to project root |
| `FEISHU_DEDUP_MESSAGE_TIMEOUT_IN_MIN` | `360` | Dedup window in minutes |

## Session Management

- Keys: `cli:default` (CLI), `lark:thread:*` / `lark:chat:*:user:*` (bot)
- **Resume** if: same prompt hash, same work dir, turns < max, TTL not expired
- On resume failure: auto-starts new session with last 6 turns as context
- Editing `prompt` invalidates all sessions (hash changes)

## Codex CLI Invocation

```bash
# new
codex exec --sandbox read-only --json --model <model> -c model_reasoning_effort="medium" -o <reply> - < <prompt>
# resume
codex exec resume --json --model <model> -c model_reasoning_effort="medium" -o <reply> <session_id> - < <prompt>
```

Answer: read from reply file (`-o`), fallback to `final_answer` in JSON stdout.

## Conventions

- Module: `lab/askplanner`
- Standard `log` package, env-var-only config, no external deps beyond Lark SDK
- All paths relative to project root
