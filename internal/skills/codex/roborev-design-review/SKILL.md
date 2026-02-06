---
name: roborev:design-review
description: Review a design proposal (PRD and task list) for completeness, feasibility, and technical soundness
---

# roborev:design-review

Review a design proposal for completeness, feasibility, and technical soundness.

## Usage

```
$roborev:design-review <path-or-job-id>
```

## Instructions

When the user invokes `$roborev:design-review <path-or-job-id>`:

### 1. Locate design documents

Find the design documents to review:

- If a **file path** is given, read that file directly
- If a **job ID** is given, fetch the design output with `roborev show --job <job_id>`
- If **no argument** is given, look for design docs in `docs/design/` and review the most recent one

### 2. Review the PRD

Evaluate the product requirements document for:

- **Completeness**: Are goals, non-goals, success criteria, and edge cases defined?
- **Feasibility**: Are technical decisions grounded in the actual codebase?
- **Clarity**: Are decisions justified and understandable?
- **Missing considerations**: Security, performance, backwards compatibility, error handling

Read relevant source files to verify that the design's assumptions about the codebase are accurate.

### 3. Review the task list

Evaluate the implementation plan for:

- **Scoping**: Are stages small enough to implement and review incrementally?
- **Ordering**: Are dependencies correctly ordered?
- **Coverage**: Do the tasks cover all requirements in the PRD?
- **Testability**: Does each stage include verification steps?

### 4. Output findings

Present findings in a structured format:

```
## Design Review

### Summary
<1-2 sentence overview of the design>

### PRD Findings
- [severity] <finding description>
  Suggestion: <how to improve>

### Task List Findings
- [severity] <finding description>
  Suggestion: <how to improve>

### Missing Considerations
- <anything not covered by the design>

### Verdict
<Pass/Fail with brief justification>
```

Use severity levels: **high** (blocks implementation), **medium** (should address before starting), **low** (nice to have).

## Example

User: `$roborev:design-review docs/design/auth-redesign.md`

Agent:
1. Reads `docs/design/auth-redesign.md`
2. Reads relevant source files (`internal/auth/`, `internal/middleware/`) to verify assumptions
3. Reviews PRD completeness and feasibility
4. Reviews task list scoping and ordering
5. Outputs structured findings with severity ratings
