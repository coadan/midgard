# Midgard 2 repository guidance

Midgard 2 is a thin event-sourced session and safe-action kernel. Preserve the
accepted boundary in `docs/decisions/0001-session-kernel.md`.

## Ownership

- `internal/eventlog` owns canonical append order and projection transactions.
- Projection reducers live beside their domain owner (`session`, `action`,
  `workspace`, `observe`). Do not add a generic materializer package.
- `internal/provider` preserves native observations; it never dispatches tools
  or mutates projections directly.
- `internal/action` owns validation-through-result state and worker fences.
- `internal/workspace` owns Git worktrees and execution containment contracts.
- `internal/environment` owns reusable ENV metadata, immutable revisions,
  project bindings, and OS-keyring references; workspace owns child-process
  injection after dispatch.
- `internal/policy/featuredelivery` owns coding-specific tools, checks, budgets,
  and completion—not the kernel.

Do not introduce task or planner/implementer/reviewer kernel types, a workflow
DSL, a universal policy engine, mandatory WebSockets, snapshots without replay
measurements, or a custom grammar as the supported default without a recorded
gate decision.

## Trust invariants

- No external execution before a durable `action.committed` and fenced
  `action.dispatched` event.
- Only uncommitted intent may be revised or retracted.
- Tool results are server/tool-authored and must match current ownership.
- General commands and checks require a real containment `Sandbox`; a working
  directory alone is not containment.
- Git is canonical for source state; immutable artifacts own large payloads;
  token deltas remain in provider trace artifacts.
- Runtime environment values are injected only into shell/check child processes
  after the committed environment revision is dispatched. Secret bytes stay in
  the OS keyring and must never enter config, events, artifacts, prompts, or TUI
  output.
- Completion is evaluated from server evidence, never asserted by a model.

## Verification

Run `go test ./...`, `go test -race ./...`, `go vet ./...`, and the protocol
scorer after changes to their respective boundaries. Add a failure or reopen
test for every new durable transition.

## Local executable

After every repository change, reinstall the current checkout with
`go install ./cmd/midgard` after the relevant verification passes. Confirm
`command -v midgard` resolves the Go-installed binary and run `midgard help` as
the installation smoke test. Treat a stale or failed installation as incomplete
delivery; do not leave the terminal using an older build than the checkout just
verified.

## TUI copy

Treat conversational copy as a TUI-wide product invariant, not just an
onboarding style. Prompts, actions, status text, progress updates, empty states,
errors, help, and slash-command feedback must describe the user's actual
workflow, choice, or consequence in plain language. Prefer questions such as
"How do changes normally land in this repository?" with concrete answers over
internal enum names such as `direct` or `pull-request`. Use verbs that match what
Midgard will do and acknowledge the user's choice in the same terms. Show
technical names only when they are needed for configuration or debugging.

Errors must explain what Midgard expected, why progress stopped, and the
smallest command or choice that lets the user continue. Do not expose raw Go
errors, storage terminology, event names, or configuration enums as the primary
message. When adding or changing a user-facing TUI flow, test its important copy
and state transitions so later refactors do not silently regress into
implementation language.
