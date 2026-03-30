---
name: roborev-refine
description: Iterative review-fix loop for the current branch — reviews via daemon, fixes inline, re-reviews until passing or max iterations reached
---

# roborev-refine

Iterative review-fix loop: review the branch, fix findings, commit, re-review,
and repeat until all reviews pass or the iteration limit is reached.

Unlike `/roborev-fix` (single-pass fix without re-review), refine closes the
loop by re-reviewing after each fix to verify the findings are resolved.

## Usage

```
/roborev-refine [--base <branch>] [--type security|design] [--max-iterations <n>]
```

- `--base <branch>`: base branch to diff against (default: auto-detect)
- `--type security|design`: review type (default: standard code review)
- `--max-iterations <n>`: maximum fix-review cycles (default: 10)

## When NOT to invoke this skill

Do NOT invoke this skill when the user is presenting or pasting existing review
results, or when they only want a single review without fixing. Use
`/roborev-review-branch` for review-only and `/roborev-fix` for fix-only.

## IMPORTANT

You must **execute bash commands** to complete this task. Skip steps already
satisfied by conversation context. Defer to CLAUDE.md when it conflicts.

## Instructions

When the user invokes `/roborev-refine [--base <branch>] [--type security|design] [--max-iterations <n>]`:

### 1. Validate inputs

If a base branch is provided, verify it resolves to a valid ref:

```bash
git rev-parse --verify -- <branch>
```

If validation fails, inform the user the ref is invalid. Do not proceed.

Parse `--max-iterations` if provided (default: 10). This is the maximum number
of fix-review cycles, not the total number of reviews.

### 2. Run the initial branch review

Build the review command:

```bash
roborev review --branch --wait [--base <branch>] [--type <type>]
```

Launch this as a background task using the `Task` tool with
`run_in_background: true` so the user can continue working. Tell the user the
branch review has been submitted.

**Note:** `--wait` exits with code 1 when the verdict is Fail. This is
expected — always capture the command output regardless of exit code and
inspect it to determine pass vs fail.

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

After committing, get a re-review of the branch. The approach depends on
whether the user specified `--type` or `--base`:

**If `--type` or `--base` was specified:** Always submit an explicit branch
review to preserve those flags (a hook-enqueued review won't have them):

```bash
roborev review --branch --wait [--base <branch>] [--type <type>]
```

Launch this as a background task using the `Task` tool with
`run_in_background: true`. Extract the job ID from the `Enqueued job <id>
for ...` line.

**If using defaults (no `--type` or `--base`):** Try `roborev wait` first —
it finds any existing job for HEAD (queued, running, or done) and blocks until
completion, reusing a hook-enqueued review if one exists:

```bash
roborev wait
```

Launch this as a background task using the `Task` tool with
`run_in_background: true`.

- Exit code 0 means the review **passed**.
- Exit code 1 means either **fail verdict** or **no job found**.

If `roborev wait` reports "No job found" (the hook is not installed, or it
uses branch-mode reviews which `wait` cannot find by SHA), fall back to an
explicit review:

```bash
roborev review --branch --wait
```

**Retrieving the job ID:** depends on which path was taken:
- **`roborev wait` path**: run `roborev show --json` afterward — it returns
  the most recent review for HEAD. Extract `job_id` from the JSON.
- **`roborev review --branch --wait` path**: extract the job ID from the
  `Enqueued job <id> for ...` line in that command's output.

This job ID replaces the previous iteration's job ID for subsequent
comment/close steps.

- If the review **passed**: inform the user and stop. The branch is clean.
- If the review **failed**: continue to the next iteration (back to step 3a)
  using the new job ID.

### 4. Iteration limit reached

If the maximum iterations are exhausted and the review still fails, inform the
user how many iterations were completed, what findings remain, and suggest they
review the remaining findings manually or run `/roborev-fix` for a targeted
pass.

## Examples

**Default refine:**

User: `/roborev-refine`

Agent:
1. Launches background task: `roborev review --branch --wait`
2. Tells user: "Branch review submitted. I'll present the results when it completes."
3. Review returns verdict Fail with 2 findings (HIGH in foo.go:42, MEDIUM in bar.go:10)
4. Fixes both findings in code
5. Runs `go test ./...` — passes
6. Commits changes
7. Records comment via heredoc: `roborev comment --job 1042 -m "$(cat <<'ROBOREV_COMMENT' ... ROBOREV_COMMENT)"`
8. Closes review: `roborev close 1042`
9. Runs `roborev wait` — hook-enqueued job 1043 completes with verdict Pass
10. Tells user: "Branch review passed after 1 fix iteration. All findings resolved."

**Security review with base branch:**

User: `/roborev-refine --base develop --type security`

Agent:
1. Validates `develop`: `git rev-parse --verify -- develop`
2. Launches background task: `roborev review --branch --wait --base develop --type security`
3. Review returns verdict Fail
4. Fixes findings, tests, commits, comments, closes
5. Re-reviews: `roborev review --branch --wait --base develop --type security`
6. Review returns verdict Fail (1 remaining finding)
7. Fixes remaining finding, tests, commits, comments, closes
8. Re-reviews: `roborev review --branch --wait --base develop --type security`
9. Review returns verdict Pass
10. Tells user: "Security review passed after 2 fix iterations."

**Max iterations reached:**

User: `/roborev-refine --max-iterations 2`

Agent:
1. Submits review, gets Fail
2. Fixes findings, commits, re-reviews — still Fail
3. Fixes again, commits, re-reviews — still Fail
4. Tells user: "Reached maximum of 2 iterations. 1 finding remains: MEDIUM in baz.go:55. You can address it manually or run `/roborev-fix` for a targeted pass."

## See also

- `/roborev-review-branch` — review without fixing
- `/roborev-fix` — single-pass fix without re-review
- `/roborev-respond` — comment on a review and close it without fixing code
