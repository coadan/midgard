# Context and Compaction

Midgard treats task state as durable data rather than using a model conversation
as its primary memory. The canonical record is:

- task, event, execution, usage, and cost metadata in SQLite;
- repository state in Git worktrees;
- full command output and generated evidence in immutable artifacts;
- a bounded context packet reconstructed for each model turn.

This makes ordinary model turns disposable. A task can be resumed by rebuilding
the packet from canonical state instead of replaying an entire chat transcript.

## Active-context policy

Midgard applies the cheapest lossless reduction before considering a summary:

1. Store full command stdout and stderr as artifacts.
2. Send bounded head-and-tail previews with artifact paths and byte counts.
3. Keep a compact ledger of all command identities and artifact references.
4. Keep detailed command output only for the newest command turn.
5. Rebuild repository guidance, relevant source snippets, current diff, role
   status, feedback, and reports from their canonical sources.

The command ledger is bounded independently from provider output. Tool call and
result references remain together so a continuation never sees an orphaned
result.

## Long-task checkpoints

When packet sizing requires another layer, add a SQLite checkpoint at an event
boundary. A checkpoint should contain structured task facts:

- objective and active constraints;
- completed work and decisions;
- current blocker or next action;
- read, modified, and still-relevant files;
- important artifact references;
- the last included event ID;
- estimated tokens before and after compaction;
- trigger reason such as threshold, overflow, or manual.

Reconstruction should use the newest checkpoint, events after its boundary, and
the most recent complete command turns. The full event and artifact history
must remain available for audit and targeted retrieval.

Create a model-written narrative summary only when meaningful information
exists solely in unstructured conversation. Structured task state, Git facts,
checks, and artifact references should be derived deterministically.

On a provider context-overflow response, create or refresh the checkpoint and
retry the interrupted turn once. Record checkpoint and retry usage so external
analysis can distinguish productive task work from context-management cost.

## Design inputs

This policy adapts two useful patterns without adopting a chat transcript as
Midgard's source of truth:

- [OpenCode compaction](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/session/compaction.ts)
  prunes older tool detail first and protects a bounded recent-turn tail.
- [Pi compaction](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/compaction.md)
  retains full session history while rebuilding model context from a checkpoint
  plus messages after a stable kept-entry boundary.
