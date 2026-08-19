# Decision: attached TUI with safe-boundary steering

Date: 2026-08-13  
Status: accepted  
Owner: repository author

## Context

The private headless probe can complete a real coding turn efficiently, but its
text progress stream cannot support a coding-chat experience. The first consumer
needs to submit follow-up guidance while the agent is active, stop and reopen
work safely, and review the resulting diff and checks. A daemon, remote transport,
landing workflow, and multi-user authority boundary have no current consumer.

The kernel already owns durable controls, actions, turns, messages, workspace
bindings, and completion evidence. Steering must not weaken the rule that a
committed action cannot be revised or retracted.

## Decision

We will ship an attached, repository-local Bubble Tea TUI over the existing
kernel. The client owns the worker process and uses typed in-process activity
events for presentation; canonical events and domain projections remain the
only durable authority.

Steering uses this ordering contract:

1. `control.steer` durably records content and means queued.
2. The provider request or dispatched action already in flight may finish.
3. While a steer is unacknowledged, `action.committed` is transactionally
   rejected. Uncommitted calls from a superseded model response are retracted or
   never materialized and receive synthetic provider tool results.
4. The coordinator inserts steering content into the next provider context.
5. `control.acknowledged` means incorporated, not merely received.

One coordinator call executes one turn and leaves the session active. Controlled
exit interrupts the turn and retains the worktree. Reopen fences lost workers;
an action dispatched by a lost worker becomes a failed
`worker_lost_outcome_unknown` action and is never automatically rerun.

The first TUI ends at a green, reviewable worktree. It does not commit, merge,
push, create a pull request, run detached, or expose remote control.

## Consequences

The user can steer safely and continue multiple turns in one worktree without a
new service or transport. The text CLI remains available through `-headless`,
and typed activity makes the terminal presentation replaceable.

Terminal loss stops progress until the user reopens Midgard. Recovery can report
unknown command outcomes but cannot infer arbitrary host side effects. The
unsafe-local execution exception remains unchanged, so this design is still
limited to the repository author and disposable worktrees.

Revisit detached execution only if attached stop/resume is measurably disruptive.
Add landing controls only after their repository-guidance and external-authority
contracts are planned separately.
