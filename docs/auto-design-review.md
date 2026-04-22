# Auto Design Review

Off by default. When enabled, roborev decides per-commit whether the commit
warrants a design review, using fast path/size/message heuristics first and
a classifier agent fallback for ambiguous cases.

## Enabling

Add to `~/.roborev/config.toml` (global) or `.roborev.toml` (per-repo):

```toml
[auto_design_review]
enabled = true
```

That's it — the defaults pick up schema/migration files, large diffs, spec
paths, and common non-design commit prefixes.

## Classifier agent

The classifier requires an agent with native schema-constrained output
AND a way to deny shell/file tool use during classification (the
classifier reads untrusted commit text, so defense-in-depth against
prompt injection matters). Supported today:

- `claude-code` (via `--json-schema` plus the `--tools ""` deny-all
  flag)

Codex is explicitly NOT supported as `classify_agent` or
`classify_backup_agent` on this branch: the codex CLI has no
documented flag that disables shell/file access during a single
invocation, so routing untrusted commit text through it would give a
prompt-injection attacker real side effects. `ValidateClassifyAgent`
rejects codex at daemon startup.

Set via:

```toml
classify_agent = "claude-code"
classify_reasoning = "fast"
# classify_backup_agent must also be a supported SchemaAgent —
# leave unset (default) until a second candidate lands.
```

If you configure an agent without schema support (gemini, copilot, cursor,
etc.), the daemon errors at startup with a clear message naming the valid
choices.

## Tuning

```toml
[auto_design_review]
min_diff_lines = 10
large_diff_lines = 500
large_file_count = 10

trigger_paths = [
  "**/migrations/**",
  "**/schema/**",
  "**/*.sql",
  "docs/superpowers/specs/**",
  "docs/design/**",
  "docs/plans/**",
  "**/*-design.md",
  "**/*-plan.md",
]
skip_paths = [
  "**/*.md",
  "**/*_test.go",
  "**/*.spec.*",
  "**/testdata/**",
]

trigger_message_patterns = [
  '\b(refactor|redesign|rewrite|architect|breaking)\b',
]
skip_message_patterns = [
  '^(docs|test|style|chore)(\(.+\))?:',
]
```

List fields replace wholesale — if you set `trigger_paths` in repo config,
the global defaults are NOT merged.

## What you'll see

- Commits that **trigger**: a design review job appears alongside the
  standard review, normally queued or running.
- Commits that **skip via heuristic**: a `skipped` design row appears with a
  reason like "doc/test-only change" or "trivial diff".
- Commits that go through the **classifier**: a `classify` job runs briefly,
  then either a design review appears or a skipped row does.

## Interaction with existing controls

- `roborev review --type design` always runs — auto mode is not consulted.
- `"design"` in `[ci].review_types` always runs — auto mode bypassed.
- Auto mode only ever adds a design review; it never alters the standard
  review, security review, or any other job.

## Observability

When enabled (globally or for any registered repo), `GET /api/status`
includes an `auto_design` subobject with per-outcome counters:

```json
{
  "auto_design": {
    "enabled": true,
    "triggered_heuristic": 3,
    "skipped_heuristic": 7,
    "triggered_classifier": 1,
    "skipped_classifier": 2,
    "classifier_failed": 0
  }
}
```

When disabled everywhere, the field is omitted entirely.
