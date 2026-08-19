# Midgard documentation

The root [README](../README.md) is the product introduction and local starting
point. This index routes deeper questions to their canonical owner.

## Use Midgard

- [Terminal chat](tui.md) — start, steer, stop, resume, review, and understand
  the terminal interface contract.
- [Agent skills](skills.md) — skill discovery, bounded reference retrieval,
  groups, and project availability masks.
- [Reusable runtime environments](decisions/0005-runtime-environments.md) —
  central environment metadata, OS-keyring secret references, inheritance, and
  child-process injection.
- [Logical projects](decisions/0004-rootless-logical-projects.md) — stable
  multi-repository identities without a shared filesystem root.

## Architecture and guarantees

- [Session kernel decision](decisions/0001-session-kernel.md) — the accepted
  kernel boundary, ownership, alternatives, and revisit gates.
- [Architecture delta](architecture/reconciled-delta.md) — the concise
  ownership and recovery model.
- [Action state transitions](action-transitions.md) — the durable intent through
  result lifecycle and its fencing rules.
- [Canonical event envelope](event-envelope.md) — ordered-event fields and
  artifact boundary.
- [Bragi model protocol](decisions/0006-bragi-model-protocol.md) — the pinned
  model-facing protocol and its relationship to Midgard action authority.
- [Context quality budget](decisions/0007-context-quality-budget.md) — bounded
  working context and deterministic compaction.
- [Local Codex bridge](decisions/0008-local-codex-model-bridge.md) — the
  model-only provider boundary and local authentication ownership.

## Current scope and history

- [Implementation status](implementation-status.md) — current stages, evidence,
  unresolved risks, and expansion gates.
- [Unsafe local prototype](decisions/0002-unsafe-local-prototype.md) — the
  temporary direct-host executor exception and removal requirements.
- [Attached TUI and steering](decisions/0003-attached-tui-and-safe-boundary-steering.md)
  — durable steering, interruption, and recovery semantics.
- [Stop and pivot ledger](stop-pivot-ledger.md) — hypotheses that can constrain
  or change the architecture.
- [Change summary](change-summary.md) — historical implementation milestones;
  use current decisions and status for present behavior.
