# AGENTS.md

Guidance for agents working in this repository.

## Project Shape

Midgard is planned as a local, Git/worktree-native coding-agent harness.

Core direction:

- Go implementation.
- One `midgard` binary.
- Local server owns task state, SQLite, artifacts, leases, workers, policies,
  command execution, SSE, and browser app serving.
- Vite + TypeScript + React browser app is the first rich client.
- Use shadcn/ui-style owned components under `web/src/components/ui`.
- Use selected AI Elements-style owned components for chat primitives, but keep
  Midgard's task store, event stream, and artifact model canonical.
- Tagged frames are the live transport; safe MDX report artifacts are the
  assembled human output for planner, implementer, and reviewer work.
- Use top-level stream tags such as `@report`, `@payload`, `@edit`, `@cmd`,
  `@ref`, and `@result`; do not use an `@m:` prefix.
- The role-facing protocol source of truth is
  `.dev/plans/agent-stream-protocol-spec.md`.
- Stream parsing must enforce output budgets and bounded repairs; incomplete
  reports or payloads are not accepted as completed role output.
- Substantial model-generated edit content should stream into payload artifacts
  and be applied through audited commands. Git diff is the source of truth for
  source-file changes.
- Avoid making models emit large JSON; JSON artifacts are mostly server-owned
  metadata and evidence.
- DeepSeek V4 is the initial low-cost provider target.
- Ygg is an optional external context dependency, not a Git submodule.
- Planner, implementer, reviewer, and compactor can use separate role model
  selections.

The product is the harness and evidence trail, not the chat UI alone.

## Local Files

- `.env` contains local secrets and must stay ignored.
- `.dev/` is ignored and holds local plans, reports, and scratch material.
- Current local plans are under `.dev/plans/`.
- Do not move ignored plans into tracked docs unless explicitly asked.

## Editing Rules

- Keep changes scoped.
- Prefer Go standard library until there is a clear reason for a dependency.
- Use `apply_patch` for manual file edits.
- Do not print secrets from `.env` or copied provider config.
- Do not commit local runtime state, worktrees, generated artifacts, or reports.

## Planned Layout

Use this layout when scaffolding code:

```text
cmd/midgard/
internal/app/
internal/artifact/
internal/benchmark/
internal/cli/
internal/command/
internal/config/
internal/cost/
internal/forge/
internal/gitrepo/
internal/model/
internal/policy/
internal/review/
internal/server/
internal/state/
internal/stream/
internal/task/
internal/webassets/
internal/workbench/
internal/ygg/
web/
migrations/
testdata/
```

See `.dev/plans/project-layout-plan.md` for the working layout plan.
Use `.dev/plans/v1-staged-build-plan.md` as the staged implementation
checklist.

## Implementation Priorities

Build the smallest vertical slice first:

1. Workbench init.
2. Repo registration.
3. Task creation.
4. Task-owned Git worktree creation.
5. SQLite state and artifact writes.
6. DeepSeek provider worker.
7. Tagged stream parsing.
8. Local HTTP+SSE server.
9. React browser client with slash commands, streamed output, artifact
   workspace, and MDX task report rendering.
10. Embedded browser assets served by the local server.
11. Usage records and final task cost output.

## Harness Invariants

- Server state is canonical.
- Git owns code history, branches, diffs, patches, and worktrees.
- SQLite indexes state and artifact metadata.
- Filesystem stores worktrees and large artifacts.
- Browser UI and CLI are clients, not orchestration owners.
- Provider workers normalize streams and usage but cannot bypass policy.
- Shell commands are the primary execution surface for agent operations.
- Cost is computed from observed usage, not projected remaining work.
- Ygg integration must be replaceable and Midgard must run without it in
  degraded Git/file/lexical context mode.

## Validation

Once code exists:

- Run `go test ./...` before finishing Go changes.
- Run focused tests for touched packages when the full suite is unnecessary or
  too slow.
- For browser changes, verify streamed output rendering, slash command handling,
  and safe MDX task report rendering.

## Code Mode Tool Batching

When `functions.exec` is available, run independent tool calls concurrently
within one bounded stage. Prefer `await Promise.allSettled([...])` and inspect
every result. `Promise.all(...)` rejects early but does not cancel calls that
already started, so use it only when discarding other results is intentional.
Keep dependencies, waits/resumes, approvals, adaptive investigations,
conflicting mutations, and builds or mutations that write the same outputs
sequential. Do not split otherwise batchable inspections across outer calls.

Keep each nested call's output bounded. Prefer focused queries and per-call
output limits; broad outputs that can truncate task evidence are not a valid
efficiency gain. If a result is truncated, narrow or page only that result
instead of rerunning the whole batch.

## Context-Efficient Coding

- Understand the task and trace the real flow first. Then stop at the first
  sufficient rung: skip speculative work, reuse repository code, use the
  standard library, use native platform features, use an installed dependency,
  and only then write the minimum new code.
- Fix root causes at the shared boundary after checking callers. Prefer
  deletion, boring code, few files, and the shortest correct diff.
- Do not add one-use abstractions, future scaffolding/config, or dependencies
  when existing code or a few direct lines suffice.
- Do not simplify away requested behavior, security, trust-boundary validation,
  data-loss/error handling, or accessibility.
- Leave the smallest runnable regression check for non-trivial logic. Mark a
  deliberate ceiling with a `ponytail:` comment and its upgrade trigger.

### Navigation-First Structure

Structure code for the shortest predictable route from entry point to
authoritative owner to verification:

- Organize by domain behavior and data ownership. Co-locate code that changes
  together; separate code with independent reasons to change.
- Give each concept and invariant one canonical owner. State the rule where it
  is enforced, make boundary contracts explicit, and keep dependencies
  directional.
- Keep entry points thin and named after the behavior they route to. Avoid
  re-export chains, mirrored representations, and dynamic indirection that
  hide the owner.
- Keep focused tests and fixtures beside or directly adjacent to the owner.
  Name files, namespaces, and symbols after domain behavior; isolate generated
  and vendor code.
- Avoid `common`, `shared`, or `utils` dumping grounds and tiny one-use file
  fragmentation. Shared code should own a stable concept, not convenience.
- When a flow must cross layers, provide one bounded map or inspection surface
  that identifies the path without requiring broad repository search.

A representative change should reach its owner and focused verification in at
most three bounded discovery stages without reading unrelated domain code.

### Evidence Before Restructuring

Optimize for the context needed to make one safe change, not file or line
count. A large file alone is not evidence. Before a structural change:

1. Trace a representative change through entry point, authoritative behavior
   and data owner, invariants, callers, shared state, and smallest verification.
2. Record files and symbols needed, why each is needed, independent change
   reasons, repeated discovery, failures, and review-driven rework.
3. Compare current and proposed navigation using available task, review, or
   session evidence.
4. Choose the smallest supported intervention: split independent ownership
   seams; rename or improve routing for discovery; improve inspection for a
   cohesive owner; extract duplicated policy; otherwise leave code together.

Structural changes require evidence of a clearer ownership or navigation
boundary. Do not add generic layers merely to shorten files.
