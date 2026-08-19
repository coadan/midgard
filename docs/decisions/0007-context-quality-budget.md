# Decision: enforce a context quality budget

Date: 2026-08-14
Phase: Develop
Status: accepted
Commitment level: consequential, reversible through policy configuration

## Context

DeepSeek V4 accepts one million tokens, but capacity is not the same as a useful
coding-agent working set. Long-context evaluations report quality degradation
inside advertised windows, coding evaluations favor focused source over broad
repository context, and established coding harnesses compact long-running
trajectories rather than filling the physical window. Midgard currently carries
every model source and host result for an active turn without a quality limit.

Canonical events, provider traces, Git state, and artifacts already preserve the
full history. The model working context is a projection and does not need to be
the evidence store.

## Decision

The feature-delivery policy uses a 128,000-token input quality limit. It starts
automatic compaction at 96,000 estimated tokens and targets at most 64,000 after
compaction. These are quality controls below the provider's physical limit, not
claims about DeepSeek's maximum capacity.

Midgard meters the most recent provider-reported prompt token count exactly and
uses a conservative estimate for content appended since that request. It shows
current context use while work runs and persists the turn's peak context use and
compaction count with billing usage.

Compaction occurs only between provider calls, after any accepted action batch
has reached a terminal result. It keeps the system prompt and current outcome,
replaces older conversation turns, active-turn protocol sources, and raw host
observations with a bounded server-authored checkpoint, and retains the latest
repair/result tail when it fits. The checkpoint contains attributed, truncated
conversation excerpts plus action identity, outcome, paths or command summaries,
hashes, and recovery instructions; it never asserts completion.

The exact compacted provider request remains in the immutable provider request
artifact. Full events, messages, traces, action results, artifacts, and Git state
are never deleted or rewritten.

## Consequences

- Long tasks have a smaller, higher-signal working set and fail before sending a
  request above the policy limit.
- A compaction changes the cached prompt prefix and can temporarily reduce cache
  hits.
- Removed file contents or skill instructions may need to be read again.
- Deterministic checkpoints preserve less semantic nuance than model-native
  compaction, but they do not introduce an unverified summary as canonical fact.

## Evidence and revisit

- DeepSeek documents a 1M physical context for V4.
- Long-context research reports degradation despite successful retrieval, and
  coding-agent research reports gains from focused context and externalized
  exploration.
- OpenAI Codex and Anthropic describe compaction and just-in-time retrieval as
  controls for long-running coding agents.

Revisit 128k after Midgard records enough completed turns to compare task
success, repair rate, provider latency, cache hit rate, and repeated reads across
context bands. Raise it only when larger bands improve outcomes; lower it when
quality or latency degrades earlier.
