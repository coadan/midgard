# Decision: adopt a reconciled thin session kernel

Date: 2026-08-08  
Phase: Develop  
Status: accepted  
Commitment level: consequential, implemented through reversible stages

## Context

Midgard V1 preserves useful audited command, artifact, worktree, output-limit,
and fenced-write behavior, but provider semantics are reduced to text deltas
and runtime ownership is organized around mutable tasks and fixed roles. The
Runtime Kernel Proposal identified sessions, ordered events, safe actions,
workspaces, and evidence as a more durable center. It did not provide evidence
that a custom model grammar, universal policy engine, WebSocket transport, or
distillation program should be foundational.

## Decision

Build Midgard 2 as a thin event-sourced session and action kernel with one
bundled Go-defined coding policy. A coding task is a policy projection over a
session, never a kernel entity. Provider-native observations are preserved in
immutable trace artifacts; only semantic boundaries become canonical events.

The action boundary is:

```text
intent -> validated -> approval_pending? -> committed -> dispatched -> result
```

Nothing executes before the exact validated intent version and idempotency key
are durably committed. Repair and retract apply only before commit. Stale worker
ownership cannot author a result.

## Alternatives

- Status quo V1: retains proven safety behavior but keeps roles, tasks, and text
  repair foundational.
- Thin task kernel: reaches the known workflow quickly but makes another task
  schema durable.
- Full runtime proposal: has a strong action model but prematurely commits to
  speculative protocol, policy, transport, and training layers.

## Evidence

Supporting:

- V1 provider streams collapse to text before runtime interpretation.
- V1 runtime behavior is dominated by role loops and mutable task tables.
- Provider APIs expose typed stream and tool-call information worth retaining.
- Audited commands, immutable evidence, Git verification, and fenced ownership
  have already proven to be useful trust boundaries.

Disconfirming:

- No current evaluation proves repairable model-authored frames outperform
  provider-native structured tool calls.
- No second materially different policy demonstrates a need for a general
  policy engine.
- No live-control implementation evidence selects WebSocket over SSE plus HTTP.

## Assumptions and uncertainty

- Session and action state can express the first coding policy without a task
  kernel type. Scenario tests check this before policy expansion.
- Replay remains affordable. Snapshots are forbidden until measurement says
  otherwise.
- Custom grammar value remains unknown. The protocol lab is the bounded probe.

## Consequences

The kernel is smaller and preserves typed evidence, while experimental choices
remain replaceable. The cost is building explicit replay, state transitions,
and failure tests. Maintainers must keep policy and transport behavior outside
kernel ownership.

## Reversibility

Protocol adapters and scorers can be discarded without changing canonical
event/action semantics. SQLite and artifact state are local and versioned.
Stages stop before server/UI or distributed rollout creates migration burden.

## Stop, pivot, and revisit

- Default permanently to native structured calls if custom encodings do not
  improve correct pre-dispatch arguments materially without task-success or
  provider-coverage regressions.
- Reconsider event sourcing if measured replay is materially slower or more
  complex than transactional recovery while providing no recovery advantage.
- Reconsider delegated runtimes if they offer equivalent pre-side-effect
  mediation and evidence at substantially lower complexity.
- Add a policy abstraction only after a second real policy exposes shared
  semantics; select live transport only after steering and approval exist.

