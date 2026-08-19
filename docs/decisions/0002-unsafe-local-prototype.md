# Decision: permit explicit unsafe local execution for the private prototype

Date: 2026-08-13  
Phase: Develop  
Status: temporary exception  
Owner: repository author

## Context

The first consumer is the repository author, working locally in disposable Git
worktrees. The immediate question is whether the session, provider, action, and
workspace pieces can complete a real coding turn with DeepSeek V4 Pro. Building
production process containment before that loop is demonstrated would not answer
the model/runtime integration question.

The accepted kernel boundary still requires containment for general commands.
This decision records a narrowly scoped prototype exception; it does not redefine
an unrestricted host process as a sandbox.

## Decision

The `midgard` executable may enable an explicit unsafe-local executor that runs
committed and fenced shell actions on the host inside the session worktree.
Unsafe execution is enabled only by the local prototype composition, is visibly
reported at startup, and is never the default behavior of `workspace.Runner`.

The following invariants remain mandatory:

- the repository starts from a committed `HEAD` and work happens in a dedicated
  Git worktree;
- no command runs before `action.committed` and `action.dispatched` are durable;
- the runner checks the current commit ID, owner, fence, session, and workspace;
- provider observations and tool results remain durable evidence;
- runtime credentials are loaded only from a selected OS-keyring provider
  profile and never written to config, events, artifacts, prompts, or tool
  output by Midgard; an environment value may be imported only through the
  explicit `midgard auth login --from-env` migration command;
- interrupt and cancellation prevent new execution.

There is no command allowlist or per-action approval in this prototype.

## Gate and removal

This exception is limited to the repository author on a local machine. Before a
second user, shared service, unattended deployment, or non-disposable workspace,
replace the unsafe executor with a containment implementation and restore the
mandatory `Sandbox` path. Stop the prototype if a command escapes the worktree,
secrets appear in recorded evidence, or stale ownership can author a result.

## Verification

The first acceptance scenario is a clean, single-commit fictional todo service.
`midgard` must create a session worktree, complete a requested edit with DeepSeek
V4 Pro, run the repository checks, and leave the source repository unchanged.
Tests must prove that unsafe execution is impossible unless the prototype
executor is supplied and that pre-dispatch and stale claims remain rejected.

The second acceptance repeats this fixture after improving the edit contract.
It additionally requires an external test unavailable to the model, exact
credential-free request traces, a provider-call ceiling, no new scratch files
outside the worktree, and a material reduction in failed actions and round trips.
