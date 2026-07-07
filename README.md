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
  --out /path/to/benchmarks/pr-123.json

go run ./cmd/midgard benchmark run \
  --root /path/to/benchmark-workbench \
  --manifest /path/to/benchmarks/pr-123.json \
  --provider deepseek \
  --model deepseek-v4-pro \
  --deepseek-reasoning-effort max \
  --max-output-tokens 4096
```

`import-pr` fetches GitHub PR metadata, writes a hidden reference patch under
`references/`, fills in `checkout_ref`, `hidden_reference_prs`,
`expected_touched_files`, and creates a default task objective from the PR title
and body. Set `GITHUB_TOKEN` or `GH_TOKEN` for private repositories or higher
GitHub API limits.

The public report includes task state, score, cost, touched files, provider
fingerprints, and artifact refs. Hidden PR metadata and reference patch paths
are written only to the `*-reference-evidence.json` sidecar.

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
