# Change summary: initial reconciled kernel

## Bragi core correction (2026-08-14)

Decision 0006 supersedes the initial provider-native default. The old native,
JSONL, compact-line, and tagged-frame adapters and comparative scorer have been
removed. Midgard now incrementally decodes provider text with Bragi's reference
implementation, records canonical protocol events before any resulting action
intent, rejects provider-native tool calls as action authority, and treats only
accepted host-action commits as executable proposals. The TUI projects the
resulting model drafts and commits without exposing the protocol name as product
copy. Provider-native chunks remain immutable transport evidence.

Behavior changed: created a new Midgard repository with a runnable local
kernel foundation and protocol scorer. No Midgard V1 files or state were
modified.

Components: canonical event log, session/turn/control reducers, immutable
artifact store, typed provider traces and normalization, action lifecycle and
ownership fencing, Git worktree tools, completion evidence, feature-delivery
policy, deterministic context view, fixtures, scenarios, schema, and CLI.

Checks and evidence: unit and integration tests cover replay/reopen, projection
rollback, duplicate/stale writes, unknown provider events, pre-commit dispatch
safety, approval, revision/retract, worker fencing, compensation, cancellation,
path escape rejection, process sandbox gating, Git edits/checks, completion,
and protocol comparison. `go test`, race detection, and vet pass.

Decisions implemented: session/event/action ownership and provider-native
default. Custom grammar, transport, general policy engine, snapshots, UI,
training, and distributed work remain gated.

Known limits: fixture scoring is not live model evidence; the repository ships
no production containment sandbox or provider adapter; no transport/client or
V1/Codex parity run is included.

Follow-up: run paired live provider evaluations; add a production sandbox;
measure long-session replay/context; only then evaluate live transport and
parity stages.

## Private headless prototype

Added durable transcript messages, an OpenAI-compatible DeepSeek V4 Pro adapter,
a thin headless turn coordinator, server-run diff/check completion evidence, and
the user-facing `midgard` executable. The coordinator composes existing domain
services; it does not introduce a task type or workflow DSL.

For the repository author's prototype only, `workspace.UnsafeHostExecutor` may
run committed and fenced commands directly on the host. It is explicit, strips
the DeepSeek credential from child environments, and leaves the normal sandbox
requirement unchanged when not supplied. The exception and removal gate are in
decision 0002.

Deterministic integration tests exercise provider call, patch action, worktree
execution, required check, Git evidence, transcript, completion, and preservation
of the source checkout.

The explicitly authorized live DeepSeek acceptance completed against the clean,
single-commit `midgard-todo` fixture. The model implemented completion and
filtering across four files, added store and HTTP failure-path tests, and left
the source checkout unchanged. Midgard recorded 85 committed/dispatched actions
(76 succeeded and 9 failed), 73 provider completion boundaries, one final
assistant message, Git diff evidence, check evidence, and an accepted completion
decision. Independent `go test -count=1 ./...`, `go test -race ./...`, and
credential-byte scans passed.

The action counts exposed the next bottleneck: DeepSeek initially emitted
`*** Begin Patch` blocks and repeatedly produced malformed raw Git hunks before
recovering. Before a TUI, align the edit-tool contract with model behavior or add
a deterministic patch-format adapter, then repeat the same fixture and require a
material reduction in failed actions and provider round trips.

## Provider credentials and layered configuration

Provider API keys now live in the operating system keyring under independent
provider/profile references. `midgard auth login`, `status`, and `logout`
manage them, and `login --from-env NAME` migrates an existing environment value
without printing it. Multiple profiles allow the same provider to have separate
keys. Runtime credential resolution uses only the selected keyring profile;
environment access is restricted to the explicit `auth login --from-env`
migration command.

Non-secret runtime defaults now merge from built-ins, the OS user config file,
and `<repo>/.midgard/config.json`; CLI flags remain authoritative. `midgard
config show` reports effective values and contributing paths. The Go module and
internal imports are named `midgard`; the misspelled checkout directory has no
effect on package identity.

## Second headless acceptance

Added a hash-fenced `file.replace` action. `file.inspect` now returns the SHA-256
of the observed bytes, replacement rejects stale hashes and path escapes, and
the write is an atomic same-directory rename that preserves the existing mode.
Patch failures now carry stable error codes; after two consecutive failures the
coordinator directs the model to inspect and replace instead. Tool descriptions
and system guidance explicitly distinguish raw Git diffs from patch-envelope
markers and require scratch work to stay in the worktree.

DeepSeek traces now preserve the exact JSON request before dispatch as well as
the native response. The request digest is its native identifier, the HTTP
authorization header is never part of the artifact, and canonical events retain
only requested/completed boundaries pointing to the immutable trace. The
coordinator also enforces a provider-call budget, exposed as
`-max-provider-calls` by the CLI.

The same clean `midgard-todo` fixture completed with thinking disabled in 9
provider calls and 17 committed/dispatched actions, all successful: 7 file
inspections, 4 hash-fenced replacements, 3 checks, 2 diffs, and 1 shell command.
This is a reduction from 73 to 9 provider calls and from 85 to 17 actions, with
failed actions reduced from 9 to 0. The session took 43.435 seconds and recorded
35,074 prompt tokens (30,976 cache hits and 4,098 misses) plus 3,577 completion
tokens; the largest prompt was 6,873 tokens.

An external Go overlay test, absent from the model's context and worktree,
verified completion, true/false filtering, invalid filter values, and the missing
todo path. Independent race tests passed, the source repository remained clean,
no new top-level `/private/tmp` scratch files appeared during the run, credential
bytes were absent from state and artifacts, and all 113 canonical events rebuilt
in 0.03 seconds. This clears the edit-contract gate before a TUI; it does not
clear the production-containment or multi-provider parity gates.

## Attached terminal chat

Added the first repository-local TUI vertical. `midgard` now opens a session home
or starts a supplied task immediately; `-headless` retains the prior text-only
turn. The chat view includes a persistent composer, typed tool cards, per-turn
provider/action usage, Git status, controlled stopping, and an evidence-backed
diff/check review.

Safe-boundary steering is durable and enforced by the kernel transaction:
queued controls block `action.committed`, already-dispatched work may finish,
remaining provider calls are superseded without execution, and acknowledgement
means the guidance is in provider context. Sessions support sequential turns in
one worktree. Reopen retracts abandoned uncommitted actions, fences lost workers,
records unknown dispatched outcomes without rerunning commands, and ends the
abandoned turn as interrupted. Decision 0003 owns this contract.

The live TUI acceptance started the clean todo fixture, queued steering while a
provider request was active, stopped the first turn, exited, reopened the active
session from the repository home, and completed a second turn in the same
worktree. Canonical ordering showed the steering control at sequence 38, the
in-flight provider boundaries at 39–40, acknowledgement at 41, and the next
provider request at 42. No action committed between queue and acknowledgement;
the next exact request trace contained two synthetic `superseded_by_steer` tool
results followed by the steering message.

Across both turns Midgard recorded 18 provider calls and 27 terminal actions (26
succeeded and one failed pre-steering replacement), with no remaining
non-terminal action. The first turn is durably interrupted, the second completed,
and the session remains active for follow-up. The TUI review showed a green gate,
checks, retained worktree, and diff. An external hidden HTTP contract test and
independent race tests passed, the source checkout stayed clean, credential bytes
were absent from state/artifacts, model shell commands stayed inside the worktree,
and all 185 events rebuilt in 0.03 seconds.

## Provider execution and configuration provenance

Provider adapters now prepare a side-effect-free native request before they may
perform network I/O. The coordinator seals that exact request in an immutable
artifact and appends its canonical `provider.requested` boundary first; only
then can the prepared call execute. A boundary probe test verifies the artifact
from inside provider execution. Native responses continue in a separate trace
artifact, preserving the request-before-I/O durability guarantee even when the
provider call fails.

Provider-specific continuation data is no longer a shared reasoning field.
Adapters own an opaque replay payload identified by adapter name; the kernel
stores it as an immutable artifact and records only its adapter and reference.
Invalid or foreign replay data stops before use, and secret bytes remain outside
this path.

`midgard config show` now reports the winning source for every non-secret field.
Runtime environment snapshots track which environment revision supplied each
effective variable, including inheritance and overrides. `midgard env status`
shows that provenance while using a deliberately value-free inspection type;
it neither reads secret bytes nor prints keyring account identifiers.

## TUI location and visual hierarchy

The repository session home and active chat now keep the selected source
repository visible, while chats and review also identify the disposable
worktree where agent actions run. New sessions load that worktree location
before the first turn begins instead of waiting for completion evidence.

A restrained terminal palette now separates navigation, paths, user and agent
speech, running work, successful evidence, and failures. Text labels and status
symbols remain authoritative so the interface still communicates without color.
Dividers and a two-line status area separate conversation, composer, metrics,
and keyboard help without introducing new interaction modes.

The same rendering pass fixed a state-refresh defect exposed by a real chat: a
failed turn briefly reported its error, then a successful transcript reload
overwrote it with `ready`. Refreshes now preserve the terminal turn outcome and
error. Running turns animate and name their current phase, including provider
waits and tool execution, so an unanswered prompt is visibly active or visibly
failed rather than silently idle.

DeepSeek chat completions now use native SSE streaming with usage included.
Every JSON chunk is preserved in sequence in the provider trace; text and
reasoning deltas are also forwarded as ephemeral live activities. The TUI
streams answer text into the conversation and pins a compact one-line thinking
preview above it. Incremental tool-call arguments are reassembled by index and
pass through the existing validation, commit, and dispatch boundary only after
the provider sends its completion marker.
