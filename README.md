# Midgard

Midgard is a local, git/worktree-centric coding-agent harness. The current V1
slice runs against one local process with SQLite state, filesystem artifacts,
audited shell commands, model stream parsing, a browser UI, and benchmark
reports.

## Prerequisites

- Go 1.25+
- Python 3
- Git
- Node.js and pnpm

Check the implementation:

```sh
go test ./...
cd web && pnpm test && pnpm build
```

## First Local Task

Initialize a workbench and register a source checkout:

```sh
go run ./cmd/midgard workbench init --root /path/to/workbench --name local
go run ./cmd/midgard workbench add-repo --root /path/to/workbench --id repo1 --path /path/to/source --main-ref main
```

Create a task. Midgard creates an isolated Git worktree under
`.midgard/worktrees/<task-id>/<repo-id>`.

```sh
go run ./cmd/midgard task create --root /path/to/workbench --id task_demo --objective "Change README"
```

Run a fake planner step from a stream file:

```sh
cat > /tmp/plan.stream <<'EOF'
@report plan.mdx
# Plan

Update README and verify the diff.
@result status:ready artifact:plan.mdx checks:diff-check
EOF

go run ./cmd/midgard task step --root /path/to/workbench --task task_demo --role planner --provider fake --fake-stream /tmp/plan.stream
```

Run a full fake planner, implementer, reviewer loop from stream files:

```sh
go run ./cmd/midgard task run \
  --root /path/to/workbench \
  --task task_demo \
  --provider fake \
  --planner-stream /tmp/plan.stream \
  --implementer-stream /tmp/implementation.stream \
  --reviewer-stream /tmp/review.stream \
  --max-output-tokens 1024
```

The terminal output includes the final task state, patch artifact, role
statuses, usage, and computed cost. The task report also gets a Midgard run
summary with provider/model fingerprints.

Run audited commands directly when needed:

```sh
go run ./cmd/midgard command run --root /path/to/workbench --task task_demo --repo repo1 -- "go test ./..."
```

Inspect state:

```sh
go run ./cmd/midgard task status --root /path/to/workbench --task task_demo
go run ./cmd/midgard task diff --root /path/to/workbench --task task_demo --repo repo1
go run ./cmd/midgard artifact list --root /path/to/workbench --task task_demo
go run ./cmd/midgard task stream --root /path/to/workbench --task task_demo
```

## Read-Only GitHub Forge

Link a registered repo to its GitHub identity, then associate one or more pull
requests with a task:

```sh
go run ./cmd/midgard forge repo link \
  --root /path/to/workbench \
  --repo repo1 \
  --remote owner/project \
  --default-branch main

go run ./cmd/midgard forge auth status \
  --root /path/to/workbench \
  --account github-main

go run ./cmd/midgard task pr link \
  --root /path/to/workbench \
  --task task_demo \
  --repo repo1 \
  --account github-main \
  --pr 42

go run ./cmd/midgard task pr refresh \
  --root /path/to/workbench \
  --task task_demo
```

Authentication is discovered from `GH_TOKEN`, `GITHUB_TOKEN`, or the GitHub
CLI. `--auth-profile anonymous`, `env:VARIABLE`, or `gh:HOST` makes the source
explicit. Midgard stores only the profile reference and never the token.
Anonymous refreshes cannot inspect review threads, so readiness reports their
state as unknown rather than assuming there are none.

Each refresh writes immutable pull, check, review, thread, and normalized
snapshot JSON under the task artifact tree. SQLite holds the latest queryable
projection. Inspect it without another network request:

```sh
go run ./cmd/midgard task pr list --root /path/to/workbench --task task_demo
go run ./cmd/midgard task pr status --root /path/to/workbench --task task_demo
go run ./cmd/midgard task pr status --root /path/to/workbench --task task_demo --for-agent
go run ./cmd/midgard task pr checks --root /path/to/workbench --task task_demo --repo repo1 --pr 42
go run ./cmd/midgard task pr threads --root /path/to/workbench --task task_demo --repo repo1 --pr 42
```

Forge readiness is warning-only by default. Enable close/readiness blockers in
`.midgard/workbench.toml`:

```toml
[forge]
readiness_gates = true
max_snapshot_age = "15m"
```

With gates enabled, stale or missing snapshots, open or unmerged PRs, failing
checks, incomplete or unresolved review threads, review blockers, unexpected
base branches, and worktree head mismatches make task status report
`resolve-forge-blockers`.

## OSS Merged-PR Benchmarks

Benchmark suites prepare isolated source checkouts, create task worktrees, run
the planner/implementer/reviewer loop, score the final patch, and write a
public report plus a hidden reference sidecar.

Minimal manifest:

```json
{
  "id": "oss-doc-wording",
  "title": "OSS documentation wording",
  "repos": [
    {
      "id": "repo1",
      "url": "https://github.com/owner/project.git",
      "checkout_ref": "<base-commit-before-merged-pr>"
    }
  ],
  "items": [
    {
      "id": "doc-wording-001",
      "title": "Fix README wording",
      "objective": "Apply the README wording change from the merged PR.",
      "task_id": "task_doc_wording_001",
      "repo_ids": ["repo1"],
      "acceptance_checks": [
        {
          "id": "tests",
          "repo_id": "repo1",
          "command": "go test ./...",
          "timeout_seconds": 300
        }
      ],
      "expected_touched_files": ["README.md"],
      "hidden_reference_patch": "references/doc-wording-001.patch",
      "hidden_reference_prs": [
        {
          "forge": "github",
          "repo": "owner/project",
          "number": 123,
          "url": "https://github.com/owner/project/pull/123",
          "merged_commit": "<merged-commit>"
        }
      ]
    }
  ]
}
```

Run the benchmark end-to-end:

```sh
go run ./cmd/midgard benchmark import-pr \
  --repo https://github.com/owner/project \
  --pr 123 \
  --check "go test ./..." \
  --out /path/to/benchmarks/pr-123.json

go run ./cmd/midgard benchmark run \
  --root /path/to/benchmark-workbench \
  --manifest /path/to/benchmarks/pr-123.json \
  --provider deepseek \
  --model deepseek-v4-pro \
  --deepseek-reasoning-effort max \
  --max-output-tokens 4096
```

Benchmark runs are durable and resume by default. Midgard records the manifest,
provider/model options, repository base commits, item order, task identity, and
each item's current phase in SQLite. A resumed run skips completed role work,
reuses current acceptance evidence, and reruns only unfinished roles or missing,
stale, or tampered acceptance evidence. The CLI prints the stable run ID and an
`action:created`, `action:resumed`, or `action:reused` marker for every item.

Task and benchmark execution is single-writer. Midgard acquires an atomic
SQLite execution lease before provider calls, commands, acceptance checks,
feedback, cleanup, forge refreshes, or report generation. Active leases renew
every 10 seconds with a 30-second expiry and carry a monotonically increasing
fence. Competing processes exit before doing work and report the current owner,
fence, acquisition time, and expiry. `SIGINT` and `SIGTERM` release ownership
immediately; after an ungraceful process death, another process can reclaim the
resource when the lease expires.

Execution state writes validate every nested benchmark and task fence inside
the same SQLite transaction as the mutation. Provider streams and command
results also recheck ownership before materializing artifacts, so an expired
owner cannot commit evidence after a higher-fence process takes over.

Manifest, execution-option, and base-commit drift is rejected before provider
calls. To intentionally discard the existing run and its task attempts, start a
new run explicitly:

```sh
go run ./cmd/midgard benchmark run \
  --root /path/to/benchmark-workbench \
  --manifest /path/to/benchmarks/pr-123.json \
  --provider deepseek \
  --reset
```

`import-pr` fetches GitHub PR metadata, writes a hidden reference patch under
`references/`, fills in `checkout_ref`, `hidden_reference_prs`,
`expected_touched_files`, and creates a default task objective from the PR title
and body. Set `GITHUB_TOKEN` or `GH_TOKEN` for private repositories or higher
GitHub API limits.

Legacy string entries under `checks` and structured `acceptance_checks` are
authoritative: Midgard executes them after the role loop instead of trusting a
model-authored result. Structured checks can select a repo, relative working
directory, timeout, and `hidden` worker-context visibility. Each check runs in
its own disposable snapshot with a restricted read-only command policy and
bounded output. The immutable summary records the exact patch checksum,
worktree fingerprints, exit status, timeout/truncation state, and sealed output
artifacts in SQLite.

A completed candidate cannot score `pass` when required acceptance evidence is
missing, stale, tampered, or failing. Reference-patch similarity remains a
diagnostic when authoritative checks pass, allowing behaviorally correct
alternative implementations. Rerun checks without another provider call:

```sh
go run ./cmd/midgard benchmark verify \
  --root /path/to/benchmark-workbench \
  --manifest /path/to/benchmarks/pr-123.json \
  --acceptance-timeout 5m
```

The public report includes task state, score, cost, touched files, provider
fingerprints, durable benchmark run identity/status, and artifact refs. Hidden
PR metadata and reference patch paths are written only to the
`*-reference-evidence.json` sidecar.

To regenerate the report from existing task state without running a provider:

```sh
go run ./cmd/midgard benchmark report \
  --root /path/to/benchmark-workbench \
  --manifest /path/to/benchmark.json
```

Start the local API and browser UI:

```sh
go run ./cmd/midgard serve --root /path/to/workbench --addr 127.0.0.1:8765
go run ./cmd/midgard ui --root /path/to/workbench
```

## Provider Notes

DeepSeek uses the Anthropic-compatible adapter. Put `DEEPSEEK_API_KEY` in
`.env` or the process environment, then select `--provider deepseek` on
`task step`, `task run`, or `benchmark run`. Use `--model deepseek-v4-pro`
with `--deepseek-reasoning-effort max` for the V4 Pro max-reasoning profile.
DeepSeek pricing is applied automatically for `deepseek-v4-pro` and
`deepseek-v4-flash`.

Codex uses the ChatGPT Codex backend directly at
`https://chatgpt.com/backend-api/codex/responses`. Select `--provider codex`.
Midgard reads `CODEX_ACCESS_TOKEN` first, then `CODEX_HOME/auth.json` or
`~/.codex/auth.json`. This supports ChatGPT login tokens and Codex personal
access tokens; agent identity auth is not supported by the direct adapter yet.
Override the default Codex model with `--model` or `MIDGARD_CODEX_MODEL`;
otherwise Midgard uses `CODEX_HOME/config.toml` or `~/.codex/config.toml`
before falling back to its built-in Codex model default.

Codex's backend request shape does not expose `max_output_tokens`; Midgard still
enforces output budgets while parsing the tagged stream. Tests never print
provider secrets.

## Cleanup

Remove generated task runtime files:

```sh
go run ./cmd/midgard task cleanup --root /path/to/workbench --task task_demo
```
