# AGENTS.md

Bragi is a specification and experiment repository for a model-native,
autoregressive streaming protocol.

## Authority

- `docs/spec.md` owns normative protocol semantics.
- `grammar/bragi.ebnf` is the machine-oriented syntax companion to the spec.
- `profiles/` owns domain schemas; the core protocol must not absorb Midgard
  workflow policy.
- `docs/websocket-binding.md` owns the server-to-client projection. WebSocket
  details must not leak into the model emission language.
- `docs/decisions/` preserves architecturally significant decisions.

## Editing

- Keep the grammar small, left-to-right, and incrementally parseable.
- Never make model EOF, provider finish reason, or a model completion claim
  authoritative task completion.
- Keep the event log append-only. Repairs alter materialized state, not history.
- Do not dispatch side effects until the runtime accepts an explicit commit.
- Prefer stable entity IDs and references over array indexes.
- Add or update examples whenever syntax changes.
- Treat protocol performance claims as hypotheses until benchmarked.
- Preserve Bragi 1.x compatibility. Incompatible grammar or semantic changes
  require a new major protocol version and an ADR.

## Validation

- Check Markdown navigation with the repository documentation audit.
- Run `go test ./...`.
- Validate examples with `go run ./cmd/bragi-check -profile
  profiles/midgard-v1.json examples/*.bragi`.
- Benchmark syntax changes against provider-native tool calls, JSON/JSONL, and
  the Midgard tagged stream before accepting them.
