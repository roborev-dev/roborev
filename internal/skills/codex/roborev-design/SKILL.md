---
name: roborev:design
description: Design a new software feature with a PRD and detailed implementation task list
---

# roborev:design

Design a new software feature with a PRD and detailed implementation task list.

## Usage

```
$roborev:design <feature description>
```

## Description

Collaboratively designs a new software feature with the developer. Explores the existing codebase to ground all technical decisions in the project's actual architecture, conventions, and dependencies. Produces two deliverables:

1. **Product Requirements Document (PRD)** — what the feature does, technical decisions, how it operates, and ordered implementation stages.
2. **Detailed Task List** — within each stage, cleanly scoped steps covering files to add or modify, tests, config changes, environment variables, dependencies, and documentation.

Both deliverables are written to files in the project for reference during implementation.

## Instructions

When the user invokes `$roborev:design <feature description>`:

### Phase 1: Understand the request and codebase

1. **Clarify the feature request.** If the description is vague or ambiguous, ask targeted questions:
   - What problem does this solve? Who is the target user?
   - Are there constraints (performance, compatibility, backward compatibility)?
   - What is explicitly out of scope?
   - Are there existing issues, RFCs, or prior discussions to reference?

   If the description is already clear and specific, proceed without interrogating the user.

2. **Explore the codebase** to understand:
   - Project structure (directories, key files, entry points)
   - Tech stack (language, framework, build system, package manager, dependencies)
   - Existing conventions (naming, file layout, test patterns, config format, error handling)
   - Architecture (how components interact, data flow, API boundaries)
   - Related existing code that touches the area this feature will affect

3. **Identify integration points** where the new feature connects to existing code, and note which existing patterns the feature should follow.

### Phase 2: Draft the PRD

4. **Write the PRD** with these sections:

   **Overview** — One-paragraph summary of the feature and the problem it solves.

   **Goals and Non-Goals** — What the feature will and will not do. Non-goals prevent scope creep during implementation.

   **Technical Decisions** — Concrete choices about libraries, storage, protocols, APIs, and data formats. Ground every decision in what the project already uses. When the project has no precedent for a choice, present the trade-offs and recommend an option. Flag decisions that the developer should weigh in on.

   **Design and Operation** — How the feature works:
   - User perspective: commands, UI, inputs, outputs, observable behavior
   - System perspective: data flow, state transitions, concurrency, persistence
   - Error handling: what can go wrong, how each failure mode is handled
   - Edge cases: empty inputs, concurrent access, partial failures, large data

   **Implementation Stages** — Ordered phases that build on each other. Each stage must:
   - Produce a working (if incomplete) system when finished
   - Be small enough to complete in a single focused session
   - Have a clear deliverable (a runnable command, a passing test suite, a working endpoint)

5. **Present the PRD to the user** for review. Wait for feedback and incorporate it before proceeding to the task list. If the user approves without changes, continue.

### Phase 3: Build the task list

6. **For each implementation stage**, produce a task list. Each task must be cleanly scoped and include whichever of the following apply:

   - **Files**: directories and files to create or modify, with the purpose of each change
   - **Code**: functions, types, interfaces, structs, methods, or constants to add or change
   - **Tests**: unit, integration, or end-to-end tests to write, including what they verify
   - **Config**: config file changes, feature flags, default values
   - **Environment**: environment variables to add, with descriptions and defaults
   - **Dependencies**: libraries, modules, or crates to import (with version constraints if relevant)
   - **Data**: database migrations, schema changes, seed data
   - **API surface**: CLI commands, HTTP endpoints, or SDK methods to add
   - **Documentation**: README sections, doc comments, man pages, or guide pages to write or update

7. **Order tasks within each stage** so they can be executed top-to-bottom. Earlier tasks must not depend on later ones. If two tasks are independent, note that they can be done in parallel.

8. **Present the task list to the user** for review. Incorporate feedback.

### Phase 4: Write deliverables

9. **Write the PRD and task list to files.** Ask the user where they would like the files. Suggest:
   - `docs/design/<feature-slug>-prd.md`
   - `docs/design/<feature-slug>-tasks.md`

   If the project has no `docs/` directory, offer to create it or suggest the project root.

10. **Offer next steps:**
    - Start implementing stage 1
    - Commit the design documents
    - Refine a specific section

## Guidelines

- **Ground decisions in the codebase.** Match the project's existing patterns, tools, and conventions. Do not introduce new frameworks, languages, or paradigms without strong justification.
- **Keep stages small and incremental.** Each stage should add clear, demonstrable value and leave the system in a working state.
- **Be specific in tasks.** "Add error handling" is too vague. "Add an error return to `ProcessFile()` in `internal/worker/process.go` and propagate it through `RunJob()` to the caller" is actionable.
- **Flag risks and open questions.** If a decision has meaningful trade-offs, present the options with pros and cons and let the developer choose.
- **Do not over-scope.** The feature description is the scope. Do not introduce tangential improvements, refactors, or "nice to haves" unless the developer asks for them.
- **Respect the project's complexity budget.** A simple feature gets a simple design. Do not add abstractions, extension points, or configurability beyond what is needed now.

## Example

User: `$roborev:design webhook notifications for review completion`

Agent:

1. Asks clarifying questions: "Should webhooks support multiple URLs per repo? Do you need retry logic for failed deliveries? Should the payload format be configurable or fixed?"

2. Explores the codebase: reads the daemon server, config system, storage layer, and worker pool to understand how reviews are processed and completed.

3. Drafts the PRD:
   - **Overview**: Send HTTP POST notifications when code reviews complete, so external tools (CI, chat, dashboards) can react to review results.
   - **Goals**: Deliver webhooks reliably, support multiple URLs per repo, include review verdict and findings in payload. **Non-goals**: payload transformation, authentication beyond a shared secret, UI for webhook management.
   - **Technical decisions**: Use stdlib `net/http` (no new deps), store webhook config in existing TOML, add `webhook_deliveries` table to SQLite for delivery tracking.
   - **Design**: After worker marks a job as `done`, enqueue a delivery for each configured webhook URL. A delivery goroutine sends the POST, records the result, and retries up to 3 times with exponential backoff.
   - **Stages**: (1) Config and storage, (2) Delivery engine, (3) Worker integration, (4) CLI management commands.

4. Presents PRD for feedback. User says: "Skip retry logic for v1, we can add it later." Agent updates the PRD.

5. Builds the task list. Stage 1 example:
   - Add `[[webhooks]]` section to `Config` struct in `internal/config/config.go`
   - Add `WebhookConfig` type with fields: `URL`, `Secret`, `Events` in `internal/config/config.go`
   - Add `webhook_deliveries` table migration in `internal/storage/migrations.go`
   - Add `InsertWebhookDelivery` and `ListWebhookDeliveries` methods to storage in `internal/storage/webhooks.go`
   - Add tests for config parsing with webhook fields in `internal/config/config_test.go`
   - Add tests for webhook storage operations in `internal/storage/webhooks_test.go`
   - Document `[[webhooks]]` config format in README under Configuration section

6. Writes `docs/design/webhook-notifications-prd.md` and `docs/design/webhook-notifications-tasks.md`.

7. Offers: "The design documents are written. Would you like to start implementing stage 1, commit the documents, or refine anything?"
