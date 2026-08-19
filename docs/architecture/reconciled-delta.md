# Architecture delta: session and action ownership

Before: V1 tasks and fixed roles own orchestration; provider streams become
text; mutable tables and selected events split authority; tagged output can
lead toward side effects.

After: a session-scoped SQLite event sequence is the lifecycle authority.
Domain-owned reducers build session, action, workspace, and evidence views.
Provider-native streams remain immutable artifacts. An exact validated action
version is durably committed before a fenced dispatcher may execute it.

Why: preserve the proven trust boundary while avoiding another task-centric
runtime and gating speculative protocol, policy, transport, and training work.

Affected consumers: provider adapters, policy implementations, workspace
runners, future control transports, and future clients.

Compatibility/migration: none. Midgard 2 starts in a new repository and does
not read or mutate V1 state.

Failure and rollback: failed appends roll back projections atomically; reopen
replays the durable log; artifacts are checksum-addressed; stale sequence and
worker fences are rejected. Deleting local experimental state returns to a
clean instance without affecting V1.

Verification: deterministic adapter tests, fake-dispatch invariants, SQLite
reopen/rebuild tests, failure injection at action boundaries, Git containment,
and focused scenario tests.

What would make this design wrong: policy-specific state leaks into the kernel,
replay provides no recovery value, delegated runtimes mediate actions with less
complexity, or tested models gain no benefit from repairable frames.

