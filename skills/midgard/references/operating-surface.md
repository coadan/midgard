# Midgard operating surface

Use `midgard help` as the installed source of truth. This reference provides the
selection map and security consequences, not a replacement for command help.

## Sessions and TUI

- `midgard` opens the current repository's session home.
- `midgard "OBJECTIVE"` starts a chat immediately.
- `midgard -headless "OBJECTIVE"` runs one text-only turn.
- Enter submits a later turn while idle and queues steering while active.
- `/repo add PATH` adds a repository after the active turn finishes.
- `/env status` shows environment metadata without values.
- `/env use NAME` changes the environment for later turns at an idle boundary.
- `Ctrl+R` reviews diff/check evidence; `Ctrl+E` expands the latest tool card;
  `Ctrl+C` requests a controlled stop before it exits an idle TUI.

## Credentials

```sh
midgard auth login PROVIDER [--profile NAME]
midgard auth login PROVIDER --profile NAME --from-env ENV_VAR
midgard auth status PROVIDER [--profile NAME]
midgard auth list
midgard auth logout PROVIDER [--profile NAME]
```

Provider credentials live in the OS keyring. Normal runtime does not load a
`.env` file or implicitly accept provider keys from process environment.

## Runtime environments

```sh
midgard env list
midgard env create NAME [--parent NAME]
midgard env set ENVIRONMENT KEY VALUE [--description TEXT]
midgard env set-secret ENVIRONMENT KEY [--from-env NAME] [--description TEXT]
midgard env unset ENVIRONMENT KEY
midgard env use NAME [-repo PATH] [-project NAME]
midgard env status [NAME] [-repo PATH] [-project NAME]
```

Plain values live in the central environment catalog. Secret values live in the
OS keyring under versioned references. Midgard intentionally has no secret
reveal command. A child process receiving a secret can still exfiltrate it.

## Projects

```sh
midgard project list
midgard project create NAME -repo NAME=PATH [-repo NAME=PATH ...]
midgard project add-repo PROJECT NAME PATH
midgard project upgrade NAME [-repo PATH] [-add-name NAME -add-path PATH]
midgard project use PROJECT [-repo PATH]
```

Projects are logical named repository sets with no shared filesystem root. An
unconfigured repository has a deterministic implicit project ID. Upgrading it
must preserve that ID and its existing state location.

## Configuration and diagnostics

- `midgard config show [-repo PATH]` reports effective non-secret configuration
  and contributing layers.
- `midgard protocol-score -manifest testdata/protocol/manifest.json` runs the
  deterministic protocol scorer from the source repository.
- `midgard init` and `midgard rebuild` are state-management commands; inspect
  their help and exact target before using them.

On macOS, catalogs and state are under the user's application configuration
directory. Do not edit those files manually while Midgard is active.
