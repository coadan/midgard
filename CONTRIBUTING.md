# Contributing to Midgard

Midgard is an experimental local coding-agent runtime. Small, evidence-backed
changes are easier to review and safer to replay.

Before opening a pull request:

1. Keep ownership boundaries in [`AGENTS.md`](AGENTS.md) and the accepted
   decisions under [`docs/decisions`](docs/decisions) intact.
2. Add a focused regression test for each new durable transition or recovery
   behavior.
3. Run the checks below from the repository root:

   ```sh
   go test ./...
   go test -race ./...
   go vet ./...
   go run ./cmd/midgard protocol-score -manifest testdata/protocol/manifest.json
   ```

4. Reinstall and smoke-test the local executable:

   ```sh
   go install ./cmd/midgard
   midgard help
   ```

Please do not include credentials, private repositories, or user-specific
paths in issues, tests, fixtures, traces, or commits.
