![roborev](https://raw.githubusercontent.com/roborev-dev/roborev-docs/main/public/logo-with-text-light.svg)

[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docs](https://img.shields.io/badge/Docs-roborev.io-blue)](https://roborev.io)

**[Documentation](https://roborev.io)** | **[Quick Start](https://roborev.io/quickstart/)** | **[Installation](https://roborev.io/installation/)**

Continuous, non-invasive background code review using AI agents
(Claude Code, Codex, Gemini, Copilot, OpenCode). Work smarter and
faster with immediate critical feedback on your agents' work.

https://github.com/user-attachments/assets/7789eb79-8176-4f6b-a9db-f3e4a328ceb9

## Features

- **Automatic Reviews** - Reviews happen on every commit via git
  hooks, or request branch or commit range reviews using the CLI
- **Multi-Agent Support** - Works with Codex, Claude Code, Gemini, Copilot, OpenCode
- **Local & Private** - Runs entirely on your machine
- **Auto-Fix with Refine** - AI automatically addresses failed reviews
- **Interactive TUI** - Real-time review queue with vim-style navigation
- **Multi-Machine Sync** - Sync reviews across machines via PostgreSQL

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

https://github.com/user-attachments/assets/5657b98c-de24-41d4-99f2-bf1aeae34c63

## Commands

| Command | Description |
|---------|-------------|
| `roborev init` | Initialize roborev in current repo |
| `roborev tui` | Interactive terminal UI |
| `roborev status` | Show daemon and queue status |
| `roborev review <sha>` | Queue a commit for review |
| `roborev review --branch` | Review all commits on current branch |
| `roborev review --dirty` | Review uncommitted changes |
| `roborev refine` | Auto-fix failed reviews using AI |
| `roborev show [sha]` | Display review for commit |
| `roborev prompt "<text>"` | Run ad-hoc prompt with AI agent |
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

## Supported Agents

| Agent | Install |
|-------|---------|
| Codex | `npm install -g @openai/codex` |
| Claude Code | `npm install -g @anthropic-ai/claude-code` |
| Gemini | `npm install -g @google/gemini-cli` |
| Copilot | `npm install -g @github/copilot` |
| OpenCode | `npm install -g opencode-ai` |
| Droid | [factory.ai](https://factory.ai/) |

roborev auto-detects installed agents.

## Documentation

Full documentation available at **[roborev.io](https://roborev.io)**:

- [Quick Start](https://roborev.io/quickstart/)
- [Installation](https://roborev.io/installation/)
- [Commands Reference](https://roborev.io/commands/)
- [Configuration](https://roborev.io/configuration/)
- [Auto-Fixing with Refine](https://roborev.io/guides/auto-fixing/)
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
