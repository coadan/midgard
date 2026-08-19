# Implementation status

Current phase: Develop  
Decision status: accepted 2026-08-08  
Supported model protocol: Bragi 1.0 with pinned Midgard profile 1.0  
Largest unresolved risk: live acceptance has not yet calibrated protocol
prompting and repair behavior across model families.

| Stage | Status | Evidence / boundary |
|---|---|---|
| 0 — decision contract | complete | decision record, architecture delta, envelope, transition table, manifest, stop/pivot ledger |
| 1 — model protocol | first runtime vertical complete; live calibration open | Bragi reference decoder/materializer, pinned profile fingerprint, streamed drafts, durable canonical events, accepted-commit effect extraction, adversarial fixtures, and scorer |
| 2 — session kernel | complete for local single-process scope | SQLite append/reopen/rebuild, atomic projections, durable user/assistant messages, artifact checksums, pre-I/O exact provider requests, native trace recorder/normalizer, and artifact-backed adapter replay state |
| 3 — action/workspace | complete for local fenced scope | approval/commit/dispatch/result/compensation, stale-worker rejection, multiple named repository worktrees, repository-qualified bounded tools, mandatory process sandbox contract; private prototype has a recorded unsafe-local exception |
| 4 — feature-delivery policy | second live headless vertical complete | Go policy, headless coordinator, DeepSeek V4 Pro adapter, hash-fenced replacement, provider-call budget, unsafe private executor, and repeated todo-service acceptance with hidden verification |
| 5 — context/recovery | attached recovery complete | deterministic bounded event tail, Git facts/guidance, version-fenced runtime environments with per-variable provenance, per-field config provenance, multi-turn worktrees, interrupted turn recovery, and lost-worker fencing; long-session measurement remains open |
| 6 — live control transport | local attached control complete; remote deferred | durable safe-boundary steering and acknowledgements work in process; no daemon, WebSocket, or SSE+POST comparison |
| 7 — client/observability | first TUI vertical complete | repository session home, chat composer, streamed model commits, safe-boundary `/repo add` and interactive `/env`, bounded repository search/action cards/history projection, managed background shell jobs, task usage/cost/context/reasoning/throughput metadata, controlled stop, and multi-repository evidence summaries |
| 8 — parity gate | not run | needs configured V1, delegated Codex, and live Midgard 2 runners on the same corpus |
| 9 — expansion | gated | no second policy/provider, distributed workers, forge, training, or rich reporting |

The current code includes a private attached coding-chat probe, not a production
coding agent. Remote control still requires transport comparison and ownership
evidence. Advancing Stage 8 requires paired runtime execution on the same corpus;
one provider adapter is not parity evidence.

The private single-user headless prototype described in
[`decisions/0002-unsafe-local-prototype.md`](decisions/0002-unsafe-local-prototype.md)
is an evidence probe, not production containment.
