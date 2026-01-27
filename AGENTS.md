# AGENTS.md

## Purpose

This repo hosts roborev, a local daemon + CLI for AI-assisted code review. Use this guide to
stay aligned with project conventions when reviewing code or addressing feedback.

## Project Orientation

- CLI entry point: `cmd/roborev/main.go` (includes daemon via `roborev daemon run`)
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

### Critical Areas to Focus On

1. **Concurrency Safety**: 
   - Fire-and-forget goroutines (must use `sync.WaitGroup` or `errgroup`)
   - Context propagation (first parameter in blocking functions)
   - Channel safety and proper closing
   - Race conditions in shared state

2. **Error Handling**:
   - Never ignore errors (`_ = func()`)
   - Always wrap errors with context (`fmt.Errorf("context: %w", err)`)
   - Use `errors.Is` and `errors.As` for error checking
   - No silent failures or empty catch blocks

3. **Go-Specific Patterns**:
   - Interface pollution (define interfaces where used, not where implemented)
   - Pointer vs value receivers (consistent usage, avoid copying large structs)
   - Slice/map preallocation when size is known
   - Proper use of `defer` for cleanup

4. **Architectural Anti-Patterns** (see `.cursor/rules/general-llm-anti-patterns.mdc`):
   - Ghost layers (>80% delegation without value)
   - I/O in loops (must batch operations)
   - Business logic in repositories (keep CRUD pure)
   - Placeholder/dead code (no `NotImplementedError`, `TODO`/`FIXME` in production)

5. **Storage Changes**:
   - Keep migrations minimal and validate schema/queries
   - Test with realistic data volumes

6. **API Changes**:
   - Preserve HTTP/JSON conventions (no gRPC)
   - Maintain backward compatibility when possible

### When Addressing Review Feedback

- **Use tools, not descriptions**: You must use the `edit` or `write` tool to modify files. Describing changes in text is not enough.
- **Edit only relevant files**: Modify only the source files (`.go` files) that the review findings and commit diff refer to. Don't edit `.roborev.toml`, `AGENTS.md`, or other config/docs unless the review explicitly asks.
- **Update tests**: If behavior changes, update tests accordingly.
- **Build and test**: Always run `go build ./...` and `go test ./...` after making changes. Never claim code compiles without actually running a build (see anti-pattern 3.8 in rules).
- **Inspect large diffs**: If diffs are large or truncated, inspect with `git show <sha>`.

## Build/Lint/Test Commands

This is a **Go-only project**. Use these commands:

```bash
# Build
go build ./...

# Test
go test ./...

# Test with coverage
go test -cover ./...

# Lint (if golangci-lint is installed)
golangci-lint run

# Format
go fmt ./...

# Install locally
go install ./cmd/roborev
```

**Never run**: `npm test`, `pytest`, `pip install`, or other non-Go commands. This project has no Python, JavaScript, or other language dependencies.

## Config + Runtime Notes

- Config priority: CLI flags → `.roborev.toml` → `~/.roborev/config.toml`.
- Reasoning defaults: reviews = thorough, refine = standard.
- `roborev refine` runs agents without sandboxing; only use on trusted code.
- Agent: `opencode` with model `ollama/roborev-coder` (see `.roborev.toml`).
- `max_review_depth`: Controls how deep the review analyzes dependencies/call chains (default: 3).

## Rules Reference

This project uses comprehensive coding rules in `.cursor/rules/`. Key files:

- **`critical-rules-quick-reference.mdc`**: Quick reference for top 15 anti-patterns
- **`general-llm-anti-patterns.mdc`**: Universal anti-patterns across all languages
- **`go-1-21-development-standards.mdc`**: Go-specific standards and best practices
- **`go-1-21-brutal-audit.mdc`**: Go-specific audit checklist

When reviewing code, check against these rules, especially:
- Ghost layer prevention (1.1)
- Performance anti-patterns (I/O in loops, 2.1)
- Error handling (5.3)
- Concurrency safety (Go-specific, 1.2)
- False compilation claims (3.8) - never claim code compiles without running build

## Agent Configuration

The project uses **OpenCode** agent with **Ollama** backend:

- **Agent**: `opencode` (CLI-based agent)
- **Model**: `ollama/roborev-coder` (custom Ollama model)
- **Model setup**: Created from `qwen2.5-coder:32b` with `num_ctx 32768` and `temperature 0.05`

To use a different model, update `model` in `.roborev.toml` under `[agent_settings.opencode]`.

## Ollama Agent Configuration

The **Ollama** agent is RoboRev's first HTTP-based agent, connecting directly to an Ollama server via HTTP API.

### Setup Requirements

1. **Install Ollama**: Download from [ollama.ai](https://ollama.ai)
2. **Start Ollama server**: Run `ollama serve` (defaults to `http://localhost:11434`)
3. **Pull a model**: Run `ollama pull <model-name>` (e.g., `ollama pull qwen2.5-coder:32b`)

### Configuration

**Per-repo config** (`.roborev.toml`):
```toml
agent = "ollama"
model = "qwen2.5-coder:32b"
```

**Global config** (`~/.roborev/config.toml`):
```toml
default_agent = "ollama"
default_model = "qwen2.5-coder:32b"
ollama_base_url = "http://localhost:11434"  # Optional, defaults to localhost:11434
```

**Remote server**:
```toml
ollama_base_url = "http://remote-server:11434"
```

### Model Selection

- **Format**: `model-name:tag` (e.g., `qwen2.5-coder:32b`, `llama3.1`)
- **Requirement**: Model must be pulled locally before use (`ollama pull <model>`)
- **No default**: Model must be explicitly configured (fails fast with clear error if missing)

### Limitations

- **Reasoning levels**: Currently no-op (Phase 1). `WithReasoning()` is accepted but doesn't affect behavior.
- **Agentic mode**: Not supported. Ollama doesn't support tool calling like Claude/Gemini. `WithAgentic()` is accepted but always operates in read-only mode.
- **Availability checking**: Uses HTTP health check to `/api/tags` with 30-second TTL cache.

### Troubleshooting

**"ollama server not reachable"**:
- Ensure Ollama server is running: `ollama serve`
- Check base URL configuration if using remote server
- Verify network connectivity

**"model not found"**:
- Pull the model: `ollama pull <model-name>`
- Verify model name format: `model:tag` (e.g., `qwen2.5-coder:32b`)
- List available models: `ollama list`

**Connection timeout**:
- Check if Ollama server is responding: `curl http://localhost:11434/api/tags`
- Increase timeout if using slow network connection
- Verify firewall settings for remote servers
