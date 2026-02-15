![roborev](https://raw.githubusercontent.com/roborev-dev/roborev-docs/main/public/logo-with-text-light.svg)

[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docs](https://img.shields.io/badge/Docs-roborev.io-blue)](https://roborev.io)

**[Documentation](https://roborev.io)** | **[Quick Start](https://roborev.io/quickstart/)** | **[Installation](https://roborev.io/installation/)**

Continuous code review for coding agents. roborev reviews every commit
as you work, catches issues before they reach a pull request, and can
automatically fix what it finds.

https://github.com/user-attachments/assets/0ea4453d-d156-4502-a30a-45ddfe300574

## Why roborev?

AI coding agents write code fast, but they make mistakes. Most people
still operate in a "commit when it's ready" mindset, which means review
feedback comes too late to be useful. The agent has moved on and
context is lost. roborev changes this by giving your agents continuous
review feedback while they are working on your prompts:

1. **Agents commit often** - ideally every turn of work
2. **roborev reviews** each commit in the background
3. **Feed findings** into your agent sessions, or fix them autonomously with `roborev fix`

Every commit gets reviewed. Issues surface in seconds, not hours.
You catch problems while context is fresh instead of waiting for PR review.

## Features

- **Background Reviews** - Every commit is reviewed automatically via
  git hooks. No workflow changes required.
- **Auto-Fix** - `roborev fix` feeds review findings to an agent that
  applies fixes and commits. `roborev refine` iterates until reviews pass.
- **Code Analysis** - Built-in analysis types (duplication, complexity,
  refactoring, test fixtures, dead code) that agents can fix automatically.
- **Multi-Agent** - Works with Codex, Claude Code, Gemini, Copilot,
  OpenCode, Cursor, and Droid.
- **Runs Locally** - No hosted service or additional infrastructure.
  Reviews are orchestrated on your machine using the coding agents
  you already have configured.
- **Interactive TUI** - Real-time review queue with vim-style navigation.
- **Extensible Hooks** - Run shell commands on review events. Built-in
  [beads](https://github.com/steveyegge/beads) integration creates trackable issues from
  review failures automatically.

## Installation

**Shell Script (macOS / Linux):**
```bash
curl -fsSL https://roborev.io/install.sh | bash
```

**Homebrew (macOS / Linux):**
```bash
brew install roborev-dev/tap/roborev
```

**Windows (PowerShell):**
```powershell
powershell -ExecutionPolicy ByPass -c "irm https://roborev.io/install.ps1 | iex"
```

**With Go:**
```bash
go install github.com/roborev-dev/roborev/cmd/roborev@latest
```

## Quick Start

```bash
cd your-repo
roborev init          # Install post-commit hook
git commit -m "..."   # Reviews happen automatically
roborev tui           # View reviews in interactive UI
```

https://github.com/user-attachments/assets/c72d7189-9a31-4c1a-a43f-c788cbd97182

## The Fix Loop

When reviews find issues, fix them with a single command:

```bash
roborev fix                     # Fix all unaddressed reviews
roborev fix 123                 # Fix a specific job
```

`fix` shows the review findings to an agent, which applies changes and
commits. The new commit gets reviewed automatically, closing the loop.

For fully automated iteration, use `refine`:

```bash
roborev refine                  # Fix, re-review, repeat until passing
```

`refine` runs in an isolated worktree and loops: fix findings, wait for
re-review, fix again, until all reviews pass or `--max-iterations` is hit.

## Code Analysis

Run targeted analysis across your codebase and optionally auto-fix:

```bash
roborev analyze duplication ./...           # Find duplication
roborev analyze refactor --fix *.go         # Suggest and apply refactors
roborev analyze complexity --wait main.go   # Analyze and show results
roborev analyze test-fixtures *_test.go     # Find test helper opportunities
```

Available types: `test-fixtures`, `duplication`, `refactor`, `complexity`,
`api-design`, `dead-code`, `architecture`.

Analysis jobs appear in the review queue. Use `roborev fix <id>` to
apply findings later, or pass `--fix` to apply immediately.

## Review Verification

When committing frequently, reviews can accumulate unaddressed findings - some valid, some false positives due to limited context. `compact` automates verification and consolidation:

```bash
roborev compact                      # Verify and consolidate findings (background)
roborev compact --wait               # Wait for completion
roborev compact --branch main        # Compact jobs on main branch
```

`compact` uses an agent to verify each finding against the current codebase. The agent searches code, filters out false positives and already-fixed issues, consolidates related findings across multiple reviews, and creates a single consolidated review. roborev automatically marks original jobs as addressed when consolidation succeeds.

This adds a quality layer between `review` and `fix`, reducing noise and making human review easier. Check progress with `roborev status` or `roborev tui`.

## Commands

| Command | Description |
|---------|-------------|
| `roborev init` | Initialize roborev in current repo |
| `roborev tui` | Interactive terminal UI |
| `roborev status` | Show daemon and queue status |
| `roborev review <sha>` | Queue a commit for review |
| `roborev review --branch` | Review all commits on current branch |
| `roborev review --dirty` | Review uncommitted changes |
| `roborev fix` | Fix unaddressed reviews (or specify job IDs) |
| `roborev refine` | Auto-fix loop: fix, re-review, repeat |
| `roborev analyze <type>` | Run code analysis with optional auto-fix |
| `roborev compact` | Verify and consolidate unaddressed review findings |
| `roborev show [sha]` | Display review for commit |
| `roborev run "<task>"` | Execute a task with an AI agent |
| `roborev address <id>` | Mark review as addressed |
| `roborev skills install` | Install agent skills for Claude/Codex |

See [full command reference](https://roborev.io/commands/) for all options.

## Configuration

Create `.roborev.toml` in your repo:

```toml
agent = "claude-code"
review_guidelines = """
Project-specific review instructions here.
"""
```

See [configuration guide](https://roborev.io/configuration/) for all options.

## Hooks

Run custom commands when reviews complete or fail. Add to `.roborev.toml`:

```toml
[[hooks]]
event = "review.completed"
command = "notify-send 'Review done for {repo_name} ({sha})'"
```

Template variables: `{job_id}`, `{repo}`, `{repo_name}`, `{sha}`, `{verdict}`, `{error}`.

### Beads Integration

The built-in `beads` hook type creates [beads](https://github.com/steveyegge/beads) issues
from review failures, giving your agents a task queue of findings to fix:

```toml
[[hooks]]
event = "review.*"
type = "beads"
```

When a review fails or finds issues, a beads issue is created with the
job ID and a `roborev fix` command, so agents can pick it up and resolve
it autonomously.

See [hooks guide](https://roborev.io/guides/hooks/) for details.

## Supported Agents

| Agent | Install |
|-------|---------|
| Codex | `npm install -g @openai/codex` |
| Claude Code | `npm install -g @anthropic-ai/claude-code` |
| Gemini | `npm install -g @google/gemini-cli` |
| Copilot | `npm install -g @github/copilot` |
| OpenCode | `npm install -g opencode-ai` |
| Cursor | [cursor.com](https://www.cursor.com/) |
| Droid | [factory.ai](https://factory.ai/) |

roborev auto-detects installed agents.

## Documentation

Full documentation available at **[roborev.io](https://roborev.io)**:

- [Quick Start](https://roborev.io/quickstart/)
- [Installation](https://roborev.io/installation/)
- [Commands Reference](https://roborev.io/commands/)
- [Configuration](https://roborev.io/configuration/)
- [Auto-Fixing with Refine](https://roborev.io/guides/auto-fixing/)
- [Code Analysis and Assisted Refactoring](https://roborev.io/guides/assisted-refactoring/)
- [Hooks](https://roborev.io/guides/hooks/)
- [Agent Skills](https://roborev.io/guides/agent-skills/)
- [PostgreSQL Sync](https://roborev.io/guides/postgres-sync/)

## Development

```bash
git clone https://github.com/roborev-dev/roborev
cd roborev
go test ./...
make install
```

## License

MIT
