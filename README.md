# askplanner

An AI assistant for TiDB query optimizer questions.

The current runtime uses Codex CLI as the primary agent engine. askplanner's domain value comes from a generated system prompt, a curated skills corpus, a curated TiDB SQL tuning docs overlay, and access to the local TiDB source tree.

Here is an example of how to use it:
````text
[Yes VPN-ON] [2026/03/19 6:32:52 Day-078] 192 :: ~/work/askplanner_v2 ‹main*› » ./bin/askplanner_cli
askplanner v2 (backend: codex-cli, model: gpt-5.3-codex)
Type your question, or 'quit' to exit. Use 'reset' to start a new session.

> hi


Hi, I’m here. Share your SQL and `EXPLAIN ANALYZE`, and I’ll help tune it.

Refined English: Hi.

> how to optimize a query like 'select * from tbl_a where a < 10 or b > 100;'


For TiDB, this pattern is usually optimized in this order:

1. Refresh stats first (most important).
```sql
ANALYZE TABLE tbl_a;
EXPLAIN ANALYZE SELECT * FROM tbl_a WHERE a < 10 OR b > 100;
```

2. Make sure both predicates are indexable.
```sql
CREATE INDEX idx_a ON tbl_a(a);
CREATE INDEX idx_b ON tbl_a(b);
```

3. Let TiDB use union-type `IndexMerge` for `OR` (if cost model does not pick it automatically).
```sql
EXPLAIN ANALYZE
SELECT /*+ USE_INDEX_MERGE(tbl_a, idx_a, idx_b) */ *
FROM tbl_a
WHERE a < 10 OR b > 100;
```

4. If `IndexMerge` is still slower, rewrite to `UNION ALL` (keeps two range scans, avoids full scan):
```sql
SELECT * FROM tbl_a WHERE a < 10
UNION ALL
SELECT * FROM tbl_a WHERE b > 100 AND NOT (a < 10);
```

Practical notes:
- `SELECT *` often forces table row fetch (`TableRowIDScan`), so speedup might be limited. If possible, select only needed columns.
- In `EXPLAIN ANALYZE`, look for `IndexMerge ... type: union` and check actual time/rows before deciding final form.
- On TiDB v8.1+, auto choice of IndexMerge can be improved by fix control `52869`, but test carefully before enabling globally.
````

## How It Works

1. A user asks a TiDB optimizer question in the local REPL or in Lark.
2. askplanner loads the domain prompt from `prompt` or falls back to the in-process prompt builder.
3. The prompt is normalized for Codex CLI. askplanner-specific tool references such as `read_file` and `search_docs` are translated into shell-based workspace exploration rules.
4. askplanner starts a new Codex session with `codex exec` or resumes an existing one with `codex exec resume`.
5. Codex reads local skills, local TiDB docs, and local TiDB source code from the workspace and returns the answer.
6. askplanner persists the conversation's `session_id` in `.askplanner/codex_sessions.json` so later turns can reuse the same Codex thread.

The runtime is intentionally simple:
- No custom MCP tool bridge yet.
- No in-process LLM API client for the main runtime path.
- No askplanner-managed tool loop on the hot path.

## Architecture

### Runtime Path

- `cmd/askplanner` is the local REPL
- `cmd/larkbot` is the Feishu/Lark websocket bot
- `internal/codex` is the active runtime

### Core Principle

Codex CLI is the agent runtime.

askplanner is the relay and domain-context layer:
- it prepares the TiDB tuning prompt
- it normalizes that prompt for Codex
- it manages session reuse
- it wires user transport to Codex CLI

reference AGENTS.md for detailed


## Skills and Docs

askplanner still uses the same domain assets:

- `contrib/agent-rules/skills/tidb-query-tuning/references/`
- `contrib/tidb/`
- `contrib/tidb-docs/`
- `prompts/tidb-query-tuning-official-docs/`

The key distinction is that the current runtime does not expose `read_file`, `search_code`, `list_dir`, `list_skills`, `read_skill`, or `search_docs` as live model tools. Instead, the normalized prompt instructs Codex to inspect those assets directly through the local workspace.

## Prerequisites

- Go 1.23+
- Codex CLI installed and authenticated via `codex login`
- TiDB source code available at `contrib/tidb/`
- TiDB docs available at `contrib/tidb-docs/` for the docs overlay
- Skills repo available at `contrib/agent-rules/` (git submodule)
- For Lark bot: a Feishu/Lark app with websocket event subscription enabled

## Quick Start

```bash
git clone https://github.com/guo-shaoge/askplanner.git
cd askplanner
git submodule update --init --recursive

# better use version of codex-cli 0.114.0 or later
codex login

make

# by default, log ouput to ./.askplanner/askplanner.log, and session info stored in ./.askplanner/sessions.json
./bin/askplanner_cli
```

The REPL supports:
- regular questions
- `reset` to drop the local Codex session
- `quit` / `exit`

## Lark Bot

Build:

```bash
go build -o bin/askplanner_lark ./cmd/larkbot
```

Run:

```bash
FEISHU_APP_ID="cli_xxxx" \
FEISHU_APP_SECRET="xxxx" \
./bin/askplanner_lark
```

The bot computes a conversation key from:
- `thread_id` when present
- otherwise `chat_id + sender_id`
- otherwise a message-level fallback

That conversation key maps to a Codex `session_id` in the local session store.

## Configuration

The main runtime is now driven by Codex-related environment variables.

| Env Var | Default | Description |
|--------|---------|-------------|
| `CODEX_BIN` | `codex` | Codex CLI binary |
| `CODEX_MODEL` | `gpt-5.3-codex` | Codex model |
| `CODEX_REASONING_EFFORT` | `medium` | Reasoning effort passed via `model_reasoning_effort` |
| `CODEX_SANDBOX` | `read-only` | Sandbox mode for `codex exec` |
| `CODEX_PROJECT_ROOT` | `.` | Working root for Codex |
| `CODEX_PROMPT_COMMAND` | `bin/printprompt` | Prompt command, supports args such as `bin/printprompt --normalized` |
| `CODEX_SESSION_STORE` | `.askplanner/codex_sessions.json` | Session store path |
| `CODEX_MAX_TURNS` | `30` | Max turns before forcing a new Codex session |
| `CODEX_SESSION_TTL_HOURS` | `24` | Session TTL |
| `CODEX_TIMEOUT_SEC` | `120` | Timeout per Codex subprocess |
| `SKILLS_DIR` | `contrib/agent-rules/skills/tidb-query-tuning/references` | Skills path |
| `TIDB_CODE_DIR` | `contrib/tidb` | TiDB source path |
| `TIDB_DOCS_DIR` | `contrib/tidb-docs` | TiDB docs path |
| `DOCS_OVERLAY_DIR` | `prompts/tidb-query-tuning-official-docs` | Curated docs overlay |

Lark-specific variables:

| Env Var | Required | Description |
|--------|----------|-------------|
| `FEISHU_APP_ID` | Yes | Feishu app ID |
| `FEISHU_APP_SECRET` | Yes | Feishu app secret |

## Build and Verify

```bash
go build -o bin/askplanner_cli ./cmd/askplanner
go build -o bin/askplanner_lark ./cmd/larkbot
go build -o bin/printprompt ./cmd/printprompt
go test ./...
```
