![roborev](https://raw.githubusercontent.com/roborev-dev/roborev-docs/main/public/logo-with-text-light.svg)

[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docs](https://img.shields.io/badge/Docs-roborev.io-blue)](https://roborev.io)

**[Documentation](https://roborev.io)** | **[Quick Start](https://roborev.io/quickstart/)** | **[Installation](https://roborev.io/installation/)**

Continuous code review for AI coding agents. roborev runs in the
background, reviews every commit as agents write code, and surfaces
issues in seconds -- before they compound. Pull code reviews into
your agentic loop while context is fresh.

![roborev TUI](https://raw.githubusercontent.com/roborev-dev/roborev-docs/main/public/tui-hero.svg)

## How It Works

1. Run `roborev init` to install a post-commit hook
2. Every commit triggers a background review -- agents write, roborev reads
3. View findings in the TUI, feed them to your agent, or let `roborev fix` handle it

## Quick Start

```bash
cd your-repo
roborev init          # Install post-commit hook
git commit -m "..."   # Reviews happen automatically
roborev tui           # View reviews in interactive UI
```

![roborev review](https://raw.githubusercontent.com/roborev-dev/roborev-docs/main/public/tui-review.svg)

## Features

- **Background Reviews** - Every commit is reviewed automatically via
  git hooks. No workflow changes required.
- **Auto-Fix** - `roborev fix` feeds review findings to an agent that
  applies fixes and commits. `roborev refine` iterates until reviews pass.
- **Code Analysis** - Built-in analysis types (duplication, complexity,
  refactoring, test fixtures, dead code) that agents can fix automatically.
- **Multi-Agent** - Works with Codex, Claude Code, Gemini, Copilot,
  OpenCode, Cursor, Kiro, Kilo, Droid, and Pi.
- **Runs Locally** - No hosted service or additional infrastructure.
  Reviews are orchestrated on your machine using the coding agents
  you already have configured.
- **Interactive TUI** - Real-time review queue with vim-style navigation.
- **Review Verification** - `roborev compact` verifies findings against
  current code, filters false positives, and consolidates related issues
  into a single review.
- **Extensible Hooks** - Run shell commands on review events. Built-in
  [beads](https://github.com/steveyegge/beads) integration creates trackable issues from
  review failures automatically.

## The Fix Loop

When reviews find issues, fix them with a single command:

```bash
roborev fix                     # Fix all open reviews
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

## Commands

| Command | Description |
|---------|-------------|
| `roborev init` | Initialize roborev in current repo |
| `roborev tui` | Interactive terminal UI |
| `roborev status` | Show daemon and queue status |
| `roborev review <sha>` | Queue a commit for review |
| `roborev review --branch` | Review all commits on current branch |
| `roborev review --dirty` | Review uncommitted changes |
| `roborev fix` | Fix open reviews (or specify job IDs) |
| `roborev refine` | Auto-fix loop: fix, re-review, repeat |
| `roborev analyze <type>` | Run code analysis with optional auto-fix |
| `roborev compact` | Verify and consolidate open review findings |
| `roborev show [sha]` | Display review for commit |
| `roborev run "<task>"` | Execute a task with an AI agent |
| `roborev close <id>` | Close a review |
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

For webhook endpoints, use the `webhook` hook type:

```toml
[[hooks]]
event = "review.completed"
type = "webhook"
url = "https://example.com/roborev-webhook"
```

The built-in `beads` hook type creates [beads](https://github.com/steveyegge/beads)
issues from review failures, giving your agents a task queue of findings to fix:

```toml
[[hooks]]
event = "review.*"
type = "beads"
```

See [hooks guide](https://roborev.io/guides/hooks/) for details.

## Supported Agents

| Agent | Install |
|-------|---------|
| Codex | `npm install -g @openai/codex` |
| Claude Code | `npm install -g @anthropic-ai/claude-code` |
| Gemini | `npm install -g @google/gemini-cli` |
| Copilot | `npm install -g @github/copilot` |
| OpenCode | `go install github.com/opencode-ai/opencode@latest` |
| Cursor | [cursor.com](https://www.cursor.com/) |
| Kiro | [kiro.dev](https://kiro.dev/) |
| Kilo | `npm install -g @kilocode/cli` |
| Droid | [factory.ai](https://factory.ai/) |
| Pi | [pi.dev](https://pi.dev/) |

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
make lint            # run full static lint checks locally
make install
make install-hooks   # install pre-commit hook to run lint before commit
# optional ACP end-to-end smoke test (Codex adapter)
make test-acp-integration
# disable mode negotiation for adapters that do not support session modes yet
make test-acp-integration ACP_TEST_DISABLE_MODE=1
# optional adapter-specific smoke tests
make test-acp-integration-codex   # codex target auto-disables mode negotiation
make test-acp-integration-claude  # claude target auto-disables mode negotiation
make test-acp-integration-gemini  # gemini target auto-disables mode negotiation
```

## License

MIT
