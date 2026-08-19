# Midgard 2

Midgard 2 is a thin, event-sourced session and safe-action kernel for coding
agents. Sessions, turns, canonical events, immutable artifacts, workspace
bindings, actions, and completion evidence are durable kernel concepts. Bragi
is the model protocol at the provider/action boundary; tasks, roles, workflow
DSLs, and live transport choices are not kernel concepts.

The repository currently implements the accepted reversible foundation:

- the Bragi 1.0 decoder and reference materializer, with a pinned Midgard
  profile and conformance scorer;
- an ordered SQLite event log with optimistic sequence fencing and rebuildable
  domain projections;
- a content-addressed immutable artifact store and provider-native trace
  recorder;
- a durable action state machine that commits before dispatch and fences stale
  workers;
- Git worktree and bounded command primitives;
- one Go-defined `feature-delivery` policy and deterministic context assembly;
- a local headless `midgard` agent composition with durable messages, a
  DeepSeek V4 Pro adapter, evidence-gated completion, and an explicit unsafe
  local executor;
- an attached terminal chat with safe-boundary steering, multi-turn worktrees,
  resumable sessions, tool cards, and diff/check review;
- rootless logical projects that give one or more independently located Git
  repositories stable names and durable identities.

The server, remote client/transport, workflow DSL, snapshots, distributed
workers, custom-model training, and production containment are intentionally
deferred behind the gates in
[`docs/decisions/0001-session-kernel.md`](docs/decisions/0001-session-kernel.md).

## Build and verify

```sh
go test ./...
go test -race ./...
go run ./cmd/midgard protocol-score -manifest testdata/protocol/manifest.json
```

`protocol-score` verifies Bragi decoding, draft repair, rejection, commit, and
host-effect extraction against the pinned profile. Provider-native observations
remain trace evidence and never become action intent.

## Local prototype safety boundary

The user-facing executable is `midgard`. Store a provider key once in the
operating system keyring, either through a no-echo prompt or by migrating an
existing environment variable:

```sh
midgard auth login deepseek
midgard auth login deepseek --profile work --from-env DEEPSEEK_API_KEY
midgard auth status deepseek --profile work
codex login
```

Profiles mount independent keys for the same provider. Select one with
`-profile work`; `default` is used when no profile is specified. Runtime
credential lookup uses only the OS keyring. Environment variables are accepted
only by the explicit `auth login --from-env` migration command. Secrets are
never written to Midgard config, state, artifacts, or worktrees.
The TUI's `/auth` interface can launch either flow. `/model` lists DeepSeek and
the models advertised by the installed Codex app-server; local Codex login
material remains owned by Codex and is never imported into Midgard.

### Runtime environments

Named runtime environments replace duplicated project `.env` files while
allowing configuration to be reused across unrelated projects. Plain values live
in Midgard's central environment catalog; secrets live only in the OS keyring.

```sh
midgard env create shared-services
midgard env set shared-services LOG_LEVEL debug \
  --description "logging verbosity"
midgard env set-secret shared-services SENTRY_DSN \
  --description "production error reporting"

midgard env create production --parent shared-services
midgard env set production PUBLIC_URL https://example.com
midgard env use production
midgard env status
```

`set-secret` uses a no-echo prompt; `--from-env NAME` explicitly migrates an
existing process environment value. Midgard has no secret reveal command.
`env status` reports variable names, kinds, descriptions, keyring-reference
presence, and the environment revision that supplied each effective variable.
It never retrieves or displays values. Values are retrieved only for a
dispatched shell/check process.

The selected environment is remembered by logical project. A turn commits its
exact environment snapshot before dispatch, and only shell/check child processes
receive the values. Known secret values are redacted from recorded output. An
agent-controlled process can still deliberately write or transmit injected
credentials, so it is appropriate only for trusted local repositories and is
not a production containment boundary.

With no task Midgard opens the current repository's session home; a task starts
a terminal chat immediately:

```sh
go build -o midgard ./cmd/midgard
./midgard -repo /path/to/repository \
  "implement the requested change"
```

### Configuration hierarchy

Non-secret settings merge in this order, with later layers winning:

1. built-in defaults;
2. the user file reported by `midgard config show` (on macOS, under
   `~/Library/Application Support/midgard/config.json`);
3. the selected user profile under the adjacent `profiles` directory;
4. `<repo>/.midgard/config.json`;
5. `<repo>/.midgard/profiles/<profile>.json`;
6. repository-local Git settings written by conversational onboarding;
7. explicit CLI flags.

Both config files use the same optional JSON fields:

```json
{
  "provider": "deepseek",
  "profile": "work",
  "model": "deepseek-v4-pro",
  "base_url": "https://api.deepseek.com",
  "default_branch": "main",
  "landing_strategy": "direct",
  "cleanup_when_landed": true,
  "thinking": true,
  "max_tokens": 16384,
  "max_provider_calls": 24
}
```

Run `midgard config show -repo /path/to/repository` to inspect the effective
values, loaded layers, and the winning source for every field. Use `-profile
NAME` to inspect a different profile. Unknown fields are rejected, including
attempts to put an `api_key` in config.

### Projects

Starting Midgard in a Git repository needs no project setup. It receives a
stable implicit project identity and keeps repository-local state. A named
project is a centrally stored set of repository mounts; it does not require a
shared parent directory or a new project root. On macOS these files live under
`~/Library/Application Support/midgard/projects/`.

Create a project from unrelated repositories:

```sh
midgard project create midgard-development \
  -repo midgard=/path/to/midgard \
  -repo bragi=/another/path/to/bragi
```

Or preserve the current implicit project identity while adding a repository:

```sh
midgard project upgrade midgard-development \
  -repo /path/to/midgard \
  -add-name bragi -add-path /another/path/to/bragi
```

Inside an active terminal chat, the conversational form performs the same
upgrade and attaches a real worktree at the next idle turn boundary:

```text
/repo add /another/path/to/bragi
```

Midgard asks for a project name when this is the second repository. Once added,
agent tools name the repository they target, so a later turn can work across the
attached worktrees.

A repository may belong to more than one project. Midgard remembers the chosen
default in that repository's local Git config; `midgard project use` changes it.

Enter queues guidance at a safe boundary while the agent works. `Ctrl+C` clears
the composer, `/stop` requests a controlled stop, and typing `/` opens the
filtered command menu for interfaces such as `/skills` and `/env`. See
[`docs/tui.md`](docs/tui.md).

The original text-only execution remains available for automation:

```sh
./midgard -headless -repo /path/to/repository \
  "implement the requested change"
```

For a local installation with the bundled repository search and browser QA
companions, run:

```sh
sh ./scripts/install-local-bundle.sh
```

Each turn defaults to at most 24 provider calls. `repo_search` uses the bundled
Yggdrasil companion and returns bounded citations plus relevant follow-up
paths, so the agent can locate relevant code before reading it. By default it
is local lexical retrieval. The index stays under Midgard's project state and
does not use Yggdrasil's user-wide configuration. An installer can enable
Midgard-owned OpenAI-compatible embeddings without exposing their API key:

```sh
midgard auth login openai --profile personal
midgard search embeddings enable \
  --endpoint https://api.openai.com/v1/embeddings \
  --model text-embedding-3-small --dimensions 1536 \
  --provider openai --profile personal
```

Midgard writes only the endpoint/model and keyring reference to its own
configuration, then passes the secret directly to bundled Yggdrasil for a
search. `browser_run` similarly invokes only the bundled Heimdal browser QA
executable inside the session worktree. `file_inspect` returns the current
SHA-256 and `file_replace` atomically replaces an existing file only
when that hash still matches; raw unified diffs remain available through
`patch_apply`. Foreground `shell` calls default to a 60-second timeout and kill
their complete process group when cancelled or timed out. Long-lived work uses
`background=true`, which returns a session-owned handle for bounded incremental
polling and explicit stopping. Each successful DeepSeek exchange stores the exact credential-free
request and native response together in an immutable trace artifact.

This composition is deliberately unsafe: shell commands run directly on the
host without containment or per-action approval. Use it only with repositories
and credentials you trust. The limitation is recorded in
[`docs/decisions/0002-unsafe-local-prototype.md`](docs/decisions/0002-unsafe-local-prototype.md).
Durable commit-before-dispatch, worker fencing, worktree isolation, provider
traces, checks, and completion evidence remain active.

## Kernel route

The shortest route through the system is:

```text
provider observation -> raw trace artifact -> normalized protocol frame
 -> action intent -> validation/approval -> durable commit
 -> fenced workspace dispatch -> server-authored result -> evidence projection
```

Each projection reducer lives with the domain state it owns. Git is canonical
for source state; artifacts are canonical for large payloads; SQLite is
canonical for ordered events and compact projections.

## License

Midgard is released under the [MIT License](LICENSE).
