---
name: midgard
description: Operates and extends the Midgard private coding-agent runtime. Use when a task mentions the midgard CLI or TUI, Midgard sessions, projects, repository worktrees, provider credentials, runtime environments or ENV injection, safe-boundary steering, action dispatch, landing, recovery, or the Midgard repository itself.
---

# Midgard

Use Midgard as a thin session and safe-action kernel. Distinguish operating the
installed CLI from changing its implementation, and preserve the trust boundary
in both cases.

## Route the task

- For CLI/TUI operation, read [references/operating-surface.md](references/operating-surface.md), then use `midgard help` and the narrow command family involved.
- For repository changes, first locate and read the repository `AGENTS.md` and
  `docs/decisions/0001-session-kernel.md`. Read only the additional decision
  record that owns the affected surface.
- For environment or secret work, also read
  `docs/decisions/0005-runtime-environments.md` when it exists.
- For multi-repository project work, also read
  `docs/decisions/0004-rootless-logical-projects.md` when it exists.
- For model streaming, actions, prompts, or TUI protocol projections, also read
  `docs/decisions/0006-bragi-model-protocol.md` when it exists.

## Preserve the operating boundary

- Never execute an external action before durable commit and fenced dispatch.
- Keep provider observations native and credential-free in durable traces.
- Treat Bragi as the sole model protocol. Only a durably recorded accepted
  Bragi host-action commit may become a Midgard action intent; provider-native
  tool calls are trace data, never action authority.
- Treat Git as canonical source state and work only in session-owned worktrees.
- Keep secret bytes in the OS keyring. Do not place them in configuration,
  prompts, events, artifacts, logs, TUI copy, or repository files.
- Runtime environments expose variable names, kinds, and descriptions to the
  agent. Values enter only dispatched shell/check child processes.
- Do not add task-kernel types, a workflow DSL, a universal policy engine,
  snapshots, or another model grammar without an accepted gate decision.
- Explain user-visible failures in workflow language with the smallest recovery
  command; do not lead with event names, enums, or raw storage errors.

## Operate safely

1. Inspect before changing state: use `midgard auth status`, `midgard project
   list`, `midgard env status`, or `midgard config show` as appropriate.
2. Use explicit imports for existing secret environment variables. Never print
   or reveal a stored secret.
3. Expect `/repo add` and `/env use` to apply at an idle turn boundary.
4. Remember that the private prototype runs agent-authored commands directly on
   the host. An injected secret can be deliberately written or transmitted by
   that process even when ordinary output redaction is active.
5. Do not commit, push, land, delete worktrees, or mutate keyring/catalog state
   unless the user's request authorizes that operation.

## Modify and verify

Keep ownership with the existing domain package. Add a failure or reopen test
for every new durable transition and focused copy coverage for TUI changes.

After a repository change, run:

```sh
GOCACHE=/tmp/midgard-go-cache go test ./...
GOCACHE=/tmp/midgard-go-cache go test -race ./...
GOCACHE=/tmp/midgard-go-cache go vet ./...
GOCACHE=/tmp/midgard-go-cache go run ./cmd/midgard protocol-score \
  -manifest testdata/protocol/manifest.json
go install ./cmd/midgard
command -v midgard
midgard help
```

Treat a failed or stale installation as incomplete delivery.
