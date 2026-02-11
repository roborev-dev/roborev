# CLAUDE.md

## Project Overview

roborev is an automatic code review daemon for git commits. It runs locally, triggered by post-commit hooks, and uses AI agents (Codex, Claude Code) to review commits in parallel.

## General Workflow

When a task involves multiple steps (e.g., implement + commit + PR), complete ALL steps in sequence without stopping. If creating a branch, committing, and opening a PR, finish the entire chain.

## Go Development

After making any Go code changes, always run `go fmt ./...` and `go vet ./...` before committing. Stage ALL resulting changes, including formatting-only files.

## Git Workflow

Always commit after completing each piece of work — do not wait to be asked. When committing changes, always stage ALL modified files (including formatting, generated files, and ancillary changes). Run `git diff` and `git status` before committing to ensure nothing is left unstaged.

## Architecture

```
CLI (roborev) → HTTP API → Daemon (roborev daemon run) → Worker Pool → Agents
                              ↓
                          SQLite DB
```

- **Daemon**: HTTP server on port 7373 (auto-finds available port if busy)
- **Workers**: Pool of 4 (configurable) parallel review workers
- **Storage**: SQLite at `~/.roborev/reviews.db` with WAL mode
- **Config**: Global at `~/.roborev/config.toml`, per-repo at `.roborev.toml`
- **Data dir**: Set `ROBOREV_DATA_DIR` env var to override `~/.roborev`

## Key Files

| Path | Purpose |
|------|---------|
| `cmd/roborev/main.go` | CLI entry point, all commands (including daemon) |
| `internal/daemon/server.go` | HTTP API handlers |
| `internal/daemon/worker.go` | Worker pool, job processing |
| `internal/storage/` | SQLite operations |
| `internal/agent/` | Agent interface + implementations |
| `internal/config/config.go` | Config loading, agent resolution |

## Conventions

- **HTTP over gRPC**: We use simple HTTP/JSON for the daemon API
- **No CGO in releases**: Build with `CGO_ENABLED=0` for static binaries (except sqlite which needs CGO locally)
- **Test agent**: Use `agent = "test"` for testing without calling real AI
- **Isolated tests**: All tests use `t.TempDir()` for temp directories

## Commands

```bash
go build ./...           # Build
go test ./...            # Test
make install             # Install to ~/.local/bin
roborev init             # Initialize in a repo
roborev status           # Check daemon/queue
```

## Adding a New Agent

1. Create `internal/agent/newagent.go`
2. Implement the `Agent` interface:
   ```go
   type Agent interface {
       Name() string
       Review(ctx context.Context, repoPath, commitSHA, prompt string) (string, error)
   }
   ```
3. Call `Register()` in `init()`

## Database Schema

Tables: `repos`, `commits`, `review_jobs`, `reviews`, `responses`

Job states: `queued` → `running` → `done`/`failed`

## Port Handling

Daemon writes runtime info to `~/.roborev/daemon.json`:
```json
{"pid": 1234, "addr": "127.0.0.1:7373", "port": 7373}
```

CLI reads this to find the daemon. If port 7373 is busy, daemon auto-increments.

## Design Constraints

- **Daemon tasks must not modify the git working tree.** Background jobs (reviews, CI polling, synthesis) are read-only with respect to the user's repo checkout. They read source files and write results to the database only. CLI commands like `roborev fix` run synchronously in the foreground and may modify files, but nothing enqueued to the worker pool should touch the working tree. If we need background tasks that produce file changes in the future, they should operate in isolated git worktrees — that is a separate initiative.

## Style Preferences

- Keep it simple, no over-engineering
- Prefer stdlib over external dependencies
- Tests should be fast and isolated
- No emojis in code or output (except commit messages)
- Never amend commits; always create new commits for fixes
- Never push or pull unless explicitly asked by the user
- **NEVER merge pull requests.** Do not run `gh pr merge` or any equivalent. Only the user merges PRs. This is non-negotiable.
- **NEVER change git branches without explicit user confirmation**. Always ask before switching, creating, or checking out branches. This is non-negotiable.
