# Midgard terminal chat

The repository author uses the terminal interface to run, steer, resume, and
review coding turns in disposable Git worktrees.

## Use it

From a Git repository, open its session home:

```sh
midgard auth login deepseek
midgard
```

Start a new chat immediately:

```sh
midgard "implement the requested change"
```

Use `-profile NAME` to select another stored key for the configured provider.
Provider, profile, model, endpoint, and budgets may also be selected in the user
or repository config reported by `midgard config show`.

All interface copy describes workflows and consequences in conversational
language. Configuration enums and event names are reserved for configuration or
debugging details rather than used as primary prompts, statuses, or errors.

The interface remains attached to the worker. Enter submits a new turn while
idle and queues safe-boundary steering while the agent is working.

Add another Git repository to the current chat with:

```text
/repo add /path/to/repository
```

If the chat began as an implicit one-repository project, Midgard asks for a
project name and preserves the existing project and chat identity. While the
agent is working, the repository waits until the turn boundary before Midgard
creates its disposable worktree. Later turns receive repository-qualified tools
and can inspect, edit, and check every attached worktree.

Inspect or change the project's runtime environment with:

```text
/env
```

The environment picker supports prefix filtering and shows variable names,
kinds, and descriptions without values. Enter selects an environment. A change
made during an active turn waits for that turn to finish and applies to later
turns.

Use `/model` to choose an installed provider model and step through only the
effort levels advertised for that model. `Alt+Left` and `Alt+Right` change
effort directly from the composer. `/auth` shows provider login state and opens
the provider's native authentication flow. Choices made while work is active
are queued and applied automatically at the next safe turn boundary.

## Contract

- The home lists sessions for the current repository, with active sessions
  first. `n` opens a new composer and Enter reopens the selected session.
- Steering displays as queued when persisted and applied once it has been added
  to provider context. It prevents another action from committing while queued.
- `/repo add PATH` validates the Git repository, upgrades an implicit project
  when needed, and makes the new worktree available to later turns. Catalog-only
  additions are never presented as active in the chat.
- `/env` opens the environment picker. Secret values and keyring references
  never enter the interface.
- `/skills` groups technology-specific guidance and can toggle either one skill
  or a complete group with Space. General skills remain ungrouped.
- `/model` and `/auth` provide model discovery, effort selection, and native
  provider sign-in without displaying credential material.
- Finished activity uses compact, outcome-oriented blocks: `Explored` names
  files and skills read, repository and skill-reference searches stay
  line-numbered and bounded,
  `Ran` keeps a bounded command/output tree, and Git evidence groups
  edits as `Edited N files (+A -D)` with per-file counts and colored diff lines.
  Added lines use light text on a dark green background; removed lines use the
  corresponding dark red background, keeping code readable at a glance.
  Command previews distinguish the executable from quoted shell text, collapse
  routine middle output as `… +N lines`, and retain the useful trailing result.
  Foreground commands time out after 60 seconds unless they request a bounded
  override. Background commands show their job handle; later polls show only
  new output, and stopping a job terminates its complete process group.
- Home and chat keep the selected source repository visible. Internal worktree
  storage paths are not shown in home, chat, or review.
- Chat keeps a single-row composer and no persistent metadata or shortcut bar.
  The composer has no line number. Typing `/` opens a filtered command menu;
  Up/Down selects, Enter runs or inserts the command, Tab completes, and Esc closes it.
- Live activity follows the bottom only while the viewport is already there.
  Scrolling up suspends auto-follow; returning to the bottom resumes it.
- Reopening an interrupted chat inserts a transcript notice after the affected
  turn. If a dispatched command was recovered with an unknown outcome, the notice
  says to inspect repository state before continuing. Steering belongs only to
  its originating active turn and is never replayed into a later turn.
- Color reinforces—not replaces—labels and symbols: cyan identifies locations
  and active details, green marks successful evidence, amber marks work or
  choices in progress, and red marks failures that need attention.
- While a turn runs, an activity indicator names the current phase: preparing
  context, waiting for the model, running a tool, or checking completion
  evidence. After each provider response it also shows cumulative input tokens,
  cache-hit input, and output tokens for the turn. A transcript refresh must
  never replace a terminal failure with an idle `ready` state.
  The model phase is a compact instrument line: an amber activity dot, muted
  call count and cache usage, cyan input, and purple output. Persistent metadata
  retains text labels; transient status relies on these stable symbols.
- Model output incrementally materializes in one transient activity slot at the
  bottom of the conversation. Thinking shows only as `thinking`, and response
  generation only as `responding`; neither draft text is rendered. Tool previews,
  repairs, and commit validation replace one another in that same slot and
  disappear when the durable action or response takes over. Raw provider chunks
  remain in the trace artifact rather than becoming chat copy.
  File and patch drafts show only their path, line count, or change counts while
  materializing; raw code appears only after commitment through the normal
  bounded Git diff rendering. Multiline shell and check commands show their
  first line plus a script line count; heredoc bodies are never dumped into the chat.
- Conversation items render in occurrence order: user request, resulting tools,
  then the final response. Completed tools collapse to a compact summary, and
  prose wraps to the current viewport width while preserving paragraphs.
  Once a turn completes, durable usage and the current model-specific USD estimate
  render in one quiet line directly beneath the composer. Only the latest completed
  task is shown there, and the line is hidden while new work runs. Bragi may commit several host actions
  from one provider response; those actions remain one billed model call. Cache
  hit rate is visually subdued because uncached input and output drive most of the cost.
  Compact usage uses `↻` for cache hit percentage, `↑` for total input sent to
  the model, and `↓` for output received, with stable one-decimal `k`/`m` notation.
  `◇` reports reasoning tokens when the provider supplies them, and `tok/s`
  divides generated output by cumulative provider-call time across the turn.
  An amber `›` introduces user turns and a slim purple `│` gutter identifies
  assistant turns, avoiding repeated role-name headings.
  Assistant headings, colored bullet and number markers, emphasis, cyan-tinted
  inline code, and fenced code receive restrained terminal styling; the durable
  message remains unchanged.
- The presentation keeps at most 256 KiB of recent message content, 128 recent
  activity cards, 128 interruption notices, and 256 usage records in memory.
  A marker reports anything omitted or shortened. The complete transcript and
  evidence remain durable and available to model context and recovery.
- `Ctrl+C` clears the composer. `/stop` requests a controlled stop during work.
  `/quit` or `q` from home leaves Midgard.
- `Esc` returns from an idle chat to the repository session home.
- On reopen, Midgard never reruns a command owned by a lost worker. It records an
  unknown outcome, starts a fresh turn, and relies on the user and agent to
  inspect current Git and host state.

For scripts and debugging, `midgard -headless "objective"` retains the original
single-turn text interface and completes the session after its evidence gate.

This private prototype executes commands directly on the host without containment
or per-action approval. It is not suitable for another user or unattended use.
