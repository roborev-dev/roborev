# Kilo Agent Design

## Summary

Add support for the [Kilo CLI](https://kilo.ai/docs/code-with-ai/platforms/cli) as a roborev agent. Kilo's CLI (`kilo run`) is functionally equivalent to OpenCode's `opencode run` — same subcommand structure, same `--model`, `--format` flags. The implementation is intentionally cloned from `opencode.go` rather than abstracted, since kilo may drift from opencode in the future.

## CLI Mapping

| roborev concept | kilo flag |
|---|---|
| prompt | stdin (piped) |
| model | `--model provider/model` |
| agentic mode | `--auto` |
| reasoning level | `--variant` (high / minimal) |
| output format | `--format default` |

Reasoning mapping: thorough -> "high", standard -> omitted, fast -> "minimal".

## Files

1. `internal/agent/kilo.go` — `KiloAgent` struct implementing the `Agent` interface
2. `internal/agent/kilo_test.go` — tests mirroring opencode_test.go patterns
3. `internal/agent/agent.go` — add "kilo" to fallback list and error message
4. `internal/agent/agent_test_helpers.go` — add "kilo" to `expectedAgents`
5. `cmd/roborev/main.go` — add "kilo" to `--agent` flag help strings

## Output Handling

Reuse `filterOpencodeToolCallLines` from opencode.go — kilo produces the same output format. Stdout/stderr tee'd to the output writer for real-time streaming.

## Design Decisions

- **Cloned, not abstracted**: The kilo agent is a near-copy of `opencode.go`. This is intentional — kilo may diverge from opencode in the future, and a shared base struct would couple them unnecessarily.
- **`--auto` for agentic mode**: Kilo uses `--auto` to auto-approve all permissions, analogous to opencode's auto-approval in non-interactive mode.
- **`--variant` for reasoning**: Maps roborev's reasoning levels to kilo's `--variant` flag.
