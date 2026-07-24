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

In Code Mode, within each bounded stage, run independent, functions.exec-available tool calls concurrently in one functions.exec call. Use await Promise.allSettled([...]) when partial results are useful, and inspect every result; use await Promise.all([...]) only when any failure should abort the batch. Keep dependencies, waits/resumes, approvals, conflicting or interdependent mutations, and adaptive investigations where each result may change the next step sequential. Do not split otherwise batchable inspections across outer tool calls.

Keep each nested call's output bounded. Prefer focused queries and per-call
output limits; broad outputs that can truncate task evidence are not a valid
efficiency gain. If a result is truncated, narrow or page only that result
instead of rerunning the whole batch.
