# AGENTS.md

## Purpose

This repo hosts roborev, a local daemon + CLI for AI-assisted code review. Use this guide to
stay aligned with project conventions when reviewing code or addressing feedback.

## Project Orientation

- CLI entry point: `cmd/roborev/main.go`
- Daemon entry point: `cmd/roborevd/main.go`
- HTTP API: `internal/daemon/server.go`
- Worker pool + job processing: `internal/daemon/worker.go`
- SQLite storage: `internal/storage/`
- Agent interface + implementations: `internal/agent/`
- Config loading/agent resolution: `internal/config/config.go`

Runtime:
- Daemon listens on `127.0.0.1:7373` (auto-increment if busy)
- Runtime info at `~/.roborev/daemon.json`
- DB at `~/.roborev/reviews.db` (WAL mode)
- Data dir override via `ROBOREV_DATA_DIR`

## Development Preferences

- Keep changes simple; avoid over-engineering.
- Prefer Go stdlib over new dependencies.
- No emojis in code or output (commit messages are fine).
- Never amend commits; fixes should be new commits.
- Never push/pull or change branches unless explicitly asked.
- Release builds use `CGO_ENABLED=0` (SQLite requires CGO locally).

## Testing

- Tests should be fast and isolated; use `t.TempDir()`.
- Use the `agent = "test"` path to avoid calling real AI agents.
- Suggested commands: `go test ./...`, `go build ./...`, `make install`.

## Review/Refine Guidance

When reviewing or fixing issues:
- Focus on correctness, concurrency safety, and error handling in daemon/worker code.
- For storage changes, keep migrations minimal and validate schema/queries.
- For API changes, preserve HTTP/JSON conventions (no gRPC).
- When addressing review feedback, update tests if behavior changes.
- If diffs are large or truncated, inspect with `git show <sha>`.

## Config + Runtime Notes

- Config priority: CLI flags → `.roborev.toml` → `~/.roborev/config.toml`.
- Reasoning defaults: reviews = thorough, refine = standard.
- `roborev refine` runs agents without sandboxing; only use on trusted code.
