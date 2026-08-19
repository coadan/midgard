# Decision: bridge local Codex as a model-only provider

Date: 2026-08-14
Status: accepted for the private local prototype

## Context

Midgard needs provider and effort switching without duplicating the user's
existing Codex authentication. Codex app-server exposes the installed model
catalog, supported reasoning efforts, local login state, streamed assistant
text, reasoning state, and usage. A nested coding agent must not become a second
action authority beside Midgard.

## Decision

The `codex` provider uses the installed `codex app-server` and its existing
ChatGPT or API-key login. Midgard never reads or copies Codex credential files.
The bridge starts with MCP servers, apps, plugins, shell, unified execution,
browsing, computer use, and image generation disabled; supplies no dynamic
tools or workspace roots; uses an empty temporary working directory and a
read-only sandbox; and replaces the base instructions with the Midgard model
protocol prompt. If Codex nevertheless emits a native tool item or requests a
host callback, Midgard fails the provider call and accepts none of it as action
intent.

Model, provider, profile reference, and effort are selected only while no turn
is active and are recorded as `session.model_selected`. A TUI choice made while
a turn is active is queued and applied automatically after that turn reaches
the safe boundary. Reopening the session restores the selection. `/model`
discovers Codex models and each model's ordered effort levels from app-server;
`/auth` delegates sign-in to `codex login` or Midgard's OS-keyring flow.

## Consequences

Codex can emit Bragi text through the same streaming decoder as any other
provider, while Midgard remains the only dispatcher. The bridge depends on the
locally installed experimental app-server protocol, so native catalog and live
model smoke tests gate upgrades. A native-tool observation is a hard failure,
not a compatibility fallback.
