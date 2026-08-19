# Contributing to Bragi

Bragi is a protocol and reference implementation. Keep changes narrow and
preserve the separation between the normative specification, the grammar, and
the reference Go implementation described in [`AGENTS.md`](AGENTS.md).

Before opening a pull request:

1. Update the owned specification, profile, or fixture when a behavioral
   change requires it.
2. Add a focused test or conformance fixture for the behavior.
3. Run:

   ```sh
   go test ./...
   go run ./cmd/bragi-check -profile profiles/midgard-v1.json examples/*.bragi
   ```

Do not include credentials, private endpoints, or user-specific paths in
issues, examples, fixtures, or commits.

Unless explicitly stated otherwise, contributions are submitted under the
[Apache License 2.0](LICENSE).
