# Copilot Non-Interactive Permission Flags

**Issue**: [#555](https://github.com/roborev-dev/roborev/issues/555)
**Date**: 2026-03-23

## Problem

The copilot agent passes no permission or non-interactive flags. In daemon
context (no TTY), copilot's tool calls fail with "Permission denied and could
not request permission from user", producing shallow reviews where roughly half
of tool calls are skipped.

## Approach

Allow-all with deny-list, matching the tiered pattern used by Claude
(`--allowedTools`), Codex (`--sandbox read-only`), and Gemini
(`--approval-mode`).

Two modes:

- **Review mode** (default): `--allow-all-tools` + deny-list blocking writes
  and destructive git operations
- **Agentic mode** (`Agentic || AllowUnsafeAgents()`): `--allow-all-tools`
  only, no deny-list

Plus `-s` (silent) unconditionally to suppress interactive stats.

### Why not granular allow-listing?

The copilot CLI's `--allow-tool` pattern matching has inconsistent coverage for
read-only git commands (copilot-cli#1736), and the mapping between built-in
tools (file reading, glob, grep) and `--allow-tool` patterns is poorly
documented (copilot-cli#1482). Under-allowing would reproduce the same
shallow-review problem. Allow-all with deny-list is reliable and safe.

### Why not `-p`/`--prompt`?

The `-p` flag passes the prompt as a command-line argument. Review prompts can
reach 250KB, which exceeds Windows command-line limits (~8KB). Stdin piping
already works for prompt delivery; the problem is purely permission denials.

## Deny-list (review mode)

```
write                    # built-in file modification tools
shell(git push:*)        # no pushing
shell(git commit:*)      # no committing
shell(git checkout:*)    # no branch switching
shell(git reset:*)       # no resetting
shell(git rebase:*)      # no rebasing
shell(git merge:*)       # no merging
shell(git stash:*)       # no stashing
shell(git clean:*)       # no cleaning
shell(rm:*)              # no file deletion
```

Deny always takes precedence over allow in copilot's permission system.

## Changes

### `internal/agent/copilot.go`

- Add `copilotSupportsAllowAllTools()` feature detection (cached `sync.Map`,
  same pattern as `claudeSupportsDangerousFlag` and
  `codexSupportsDangerousFlag`)
- Update `Review()` to build permission args based on agentic vs review mode
- Add `-s` flag unconditionally for non-interactive output
- Update `CommandLine()` to reflect permission flags
- Fix `WithAgentic()` comment to remove "has no effect" note

### `internal/agent/copilot_test.go`

- Test review mode produces `--allow-all-tools` + deny-list + `-s`
- Test agentic mode produces `--allow-all-tools` + `-s` only (no deny-list)
- Test `AllowUnsafeAgents()` override enables agentic mode
- Test feature detection fallback (old binary without `--allow-all-tools`)

### No config changes

`allow_unsafe_agents` in `.roborev.toml` / global config already serves as the
escape hatch for users who want unrestricted tool access. No new config fields.
