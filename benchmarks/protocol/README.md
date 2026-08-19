# Model protocol conformance

Run:

```sh
go run ./cmd/midgard protocol-score -manifest testdata/protocol/manifest.json
```

The checked-in corpus exercises deterministic Bragi decoding, draft repair,
rejection, commit acceptance, and safe host-effect extraction against the
pinned Midgard profile. It is deliberately not evidence about live model
quality.

Extend the corpus with interruption, malformed source, open literals, unknown
entities, invalid references, post-commit mutation, completion proposals, and
replay cases as those runtime paths evolve. Live acceptance reports should also
measure time to useful draft and commit, repair distance, argument correctness
before dispatch, tokens, latency, cost, task success, evidence completeness,
and interruption recovery across supported model families.
