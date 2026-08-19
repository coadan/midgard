# Decision: use Bragi as Midgard's model protocol

Date: 2026-08-14
Phase: Develop
Status: accepted
Commitment level: consequential, reversible at the provider boundary

## Context

Decision 0001 kept provider-native structured tool calls as the supported
default because the protocol experiment had not yet established a better
choice. That framing omitted a product premise: Midgard is the host harness for
Bragi. Bragi is not merely an alternative tool-call encoding to score from the
outside; its progressively materialized, explicitly committed entities are the
model-facing operating protocol that Midgard is meant to mediate and expose.

Provider-native observations are still valuable evidence. They describe the
transport faithfully, including reasoning and token deltas, but they do not
define Midgard action intent.

## Decision

Midgard uses `bragi/1.0` with the exact `midgard` profile revision negotiated
for a turn. Provider output text is incrementally decoded by Bragi and applied
to Bragi's reference materializer. Midgard durably records every canonical
Bragi accepted or rejected event before it acts on any resulting effect.

Only a Bragi `commit.accepted` event whose entity profile effect is
`host-action` may be translated into a Midgard action intent. The existing
Midgard validation, commit, dispatch, worker-fence, and result boundary remains
authoritative after that translation. A Bragi completion is only a completion
proposal; the server evidence gate still decides whether the turn is complete.

Provider-native tools are not advertised in Bragi mode and provider-native tool
calls are rejected rather than treated as action intent. Native provider
chunks, reasoning, usage, and replay state remain immutable trace artifacts.

The TUI projects Bragi draft revisions, repairs, rejected records, and accepted
commits as they stream. It then projects the separate Midgard action lifecycle
for committed host effects. This makes the visible protocol lifecycle the same
one that controls execution.

The Bragi Go implementation is pinned as a vendored dependency. The negotiated
profile bytes and SHA-256 fingerprint are versioned with Midgard so a replay
does not depend on mutable files in another checkout.

This decision supersedes only the provider-native default and custom-grammar
uncertainty in decision 0001. It does not change the session kernel, provider
trace ownership, action fences, Git source-of-truth, or evidence-based
completion boundary.

## Evidence

- Bragi 1.0 already defines bounded streaming recovery, canonical records,
  repairable drafts, explicit commit proposals, runtime acceptance, replay, and
  a Midgard profile.
- Its reference materializer separates `commit.proposed` from
  `commit.accepted`; accepted effects remain eligible for host policy rather
  than executing inside Bragi.
- Midgard's action service already supplies the durable validation-through-
  result lifecycle needed after a Bragi host-action commit.
- Progressive Bragi entities are the intended source for the TUI behavior the
  current provider-native tool preview was approximating.

## Alternatives

- Keep provider-native tools and show a Bragi-like rail: visually useful, but
  the display would not represent the protocol controlling execution.
- Reimplement Bragi inside Midgard: avoids a dependency but creates semantic
  drift and a second authority for the grammar.
- Depend on an adjacent Bragi checkout at runtime: expedient locally, but makes
  clean Midgard checkouts non-reproducible.

## Consequences

Model prompts and continuation messages must speak Bragi rather than native
tool-call APIs. Protocol events become durable session evidence. Provider
adapters become simpler with respect to actions but must preserve text chunks
exactly. The TUI can show useful typed state before an entity is committed.

## Reversibility and gates

The integration is isolated between provider text and Midgard action intent.
Provider traces and Midgard action events keep their existing schemas, so a
future protocol revision can be introduced by negotiating a different pinned
profile. A Bragi or profile upgrade requires conformance, replay, failed-commit,
and no-effect-before-acceptance tests before changing the negotiated tuple.
