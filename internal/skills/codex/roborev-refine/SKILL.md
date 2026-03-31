---
name: roborev-refine
description: Iterative review-fix loop for the current branch — reviews via daemon, fixes inline, re-reviews until passing or max iterations reached
---

# roborev-refine

Iterative review-fix loop: review the current branch or commit range, fix
findings, commit, re-review, and repeat until all reviews pass or the
iteration limit is reached.

Unlike `$roborev-fix` (single-pass fix without re-review), refine closes the
loop by re-reviewing after each fix to verify the findings are resolved.

This skill should perform the refine workflow inside the current coding agent
CLI. Do not simply shell out to `roborev refine`.

## Usage

```
$roborev-refine [--since <commit>] [--branch <name>] [--max-iterations <n>]
```

- `--since <commit>`: refine commits after this commit (exclusive); required on the default branch
- `--branch <name>`: validate that the current branch matches before refining
- `--max-iterations <n>`: maximum fix-review cycles (default: 10)

This skill intentionally focuses on the current branch flow. It does not expose
`roborev refine --all-branches` or `roborev refine --list`.

## When NOT to invoke this skill

Do NOT invoke this skill when the user is presenting or pasting existing review
results, or when they only want a single review without fixing. Use
`$roborev-review-branch` for review-only and `$roborev-fix` for fix-only.

## IMPORTANT

This skill requires you to **execute bash commands** to validate inputs, launch
reviews, and wait for re-review. The task is not complete until the refine loop
finishes and you present the result to the user.

These instructions are guidelines, not a rigid script. Use the conversation
context. Skip steps that are already satisfied. Defer to project-level
CLAUDE.md instructions when they conflict with these steps.

## Instructions

When the user invokes `$roborev-refine [--since <commit>] [--branch <name>] [--max-iterations <n>]`:

### 1. Validate inputs and refine context

If `--branch` is provided, verify the current branch matches before doing any
work. If it does not, stop and tell the user.

If `--since` is provided, verify it resolves to a valid commit and is an
ancestor of `HEAD`.

If `--since` is not provided, ensure you are not refining the default branch.
This matches `roborev refine`, which refuses to run on the default branch
without `--since`.

Parse `--max-iterations` if provided (default: 10). This is the maximum number
of fix-review cycles, not the total number of reviews.

### 2. Run the initial review

Choose the review command that matches the requested scope:

```bash
roborev review --since <commit> --wait
```

or, if `--since` was not provided:

```bash
roborev review --branch --wait
```

`--since` is the closest manual equivalent to `roborev refine --since`.
`--branch` reviews the current branch relative to its merge-base.

**Note:** `--wait` exits with code 1 when the verdict is Fail. This is
expected. Always capture the command output regardless of exit code and inspect
it to determine pass vs fail.

When the review completes, read and parse the output. Extract the job ID from
the `Enqueued job <id> for ...` line or the review header — you will need it
for commenting and closing later.

If the command output contains an error (daemon not running, repo not
initialized, review errored), report it. Suggest `roborev status` to check the
daemon or `roborev init` if the repo is not initialized.

If the review **passed**, inform the user and stop. No fixes needed.

### 3. Fix-review loop

If the review **failed**, begin the iterative loop. For each iteration
(up to `--max-iterations`):

#### 3a. Fix the findings

Parse findings from the review output. Collect every finding with its severity,
file path, and line number. Then:

1. **Sort by severity**: fix HIGH findings first, then MEDIUM, then LOW
2. **Group by file**: within each severity level, batch edits to the same file
   to minimize context switches
3. If some findings cannot be fixed (false positives, intentional design), note
   them for the comment rather than silently skipping them

If a finding's context is unclear, read the relevant source files to understand
the code before making changes.

#### 3b. Run tests

Run the project's test suite to verify the fixes:

```bash
go test ./...
```

Or whatever test command the project uses. If tests fail, fix the regressions
before proceeding.

#### 3c. Commit, then record comment and close review

Commit first per the project's conventions (see CLAUDE.md). Only after the
commit succeeds, record a summary comment on the review and close it:

```bash
roborev comment --job <job_id> -m "$(cat <<'ROBOREV_COMMENT'
<summary of changes>
ROBOREV_COMMENT
)"
# Only if the comment above succeeded:
roborev close <job_id>
```

**Important:** Always pass the comment text via a heredoc as shown above, never
by interpolating dynamic text directly into a shell string. Review-derived
content may contain shell metacharacters that could cause unintended execution.

The comment should reference each finding by severity and file, state what was
fixed, and note any findings intentionally skipped. Keep it concise.

#### 3d. Re-review

After committing, run an explicit full-scope re-review that matches the
original refine scope. Do not treat a passing `roborev wait` result for the new
commit as sufficient to stop — the full branch or commit-range review must pass
before you report success.

First, check whether the post-commit hook already enqueued a review:

```bash
roborev wait
```

If `roborev wait` finds a job for `HEAD`, retrieve its details:

```bash
roborev show --json
```

Inspect the `job.git_ref` field in the JSON output to determine the hook
review's scope:

- **Branch/range ref** (contains `..`, e.g. `abc123..def456`): The hook
  enqueued a full branch review. If this matches the refine scope (same base),
  **reuse it** — use its `job_id` and verdict directly. Skip the explicit
  review below.
- **Single commit SHA**: The hook enqueued a commit-scoped review. Remember
  its `job_id` as the hook review job to close later, then proceed with the
  explicit full-scope review below.

If `roborev wait` reports "No job found", the hook is not installed — proceed
directly with the explicit review.

**Explicit full-scope review** (skip if reusing a hook branch review above):

If refining with `--since`:

```bash
roborev review --since <commit> --wait
```

If refining without `--since`:

```bash
roborev review --branch --wait
```

**Retrieving the job ID:** extract it from the
`Enqueued job <id> for ...` line in the explicit review command output.

If you recorded a hook commit review job earlier, close it after the explicit
full-scope review completes so refine does not leave stale reviews open:

```bash
roborev close <hook_job_id>
```

- If the review (reused hook or explicit) **passed**: inform the user and
  stop. The branch or requested commit range is clean.
- If the review **failed**: continue to the next iteration (back to step 3a)
  using the new job ID.

### 4. Iteration limit reached

If the maximum iterations are exhausted and the explicit full-scope review
still fails, inform the user how many iterations were completed, what findings
remain, and suggest they review the remaining findings manually or run
`$roborev-fix` for a targeted pass.

## Examples

**Default refine on a feature branch:**

User: `$roborev-refine`

Agent:
1. Validates that the current branch is not the default branch
2. Runs `roborev review --branch --wait`
3. Review returns verdict Fail with 2 findings
4. Fixes both findings in code
5. Runs `go test ./...` — passes
6. Commits changes
7. Records comment and closes the old review
8. Runs `roborev wait` — finds hook review, checks `job.git_ref`:
   - If branch ref matching scope: reuses it directly
   - If commit SHA: remembers hook job ID, runs `roborev review --branch --wait`, closes hook job
   - If no job found: runs `roborev review --branch --wait`
9. Review passes
10. Tells user: "Branch review passed after 1 fix iteration. All findings resolved."

**Refine from a specific starting commit:**

User: `$roborev-refine --since abc123 --max-iterations 3`

Agent:
1. Validates `abc123` resolves and is an ancestor of `HEAD`
2. Runs `roborev review --since abc123 --wait`
3. Review returns verdict Fail
4. Fixes findings, tests, commits, comments, closes
5. Checks for hook review via `roborev wait` — reuses if branch scope matches, otherwise runs explicit `roborev review --since abc123 --wait` and closes any commit-scoped hook review
6. Continues until the full requested range passes or 3 iterations are exhausted

## See also

- `$roborev-review-branch` — review without fixing
- `$roborev-fix` — single-pass fix without re-review
- `$roborev-respond` — comment on a review and close it without fixing code
