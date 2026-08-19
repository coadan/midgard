# Decision: rootless logical projects

Date: 2026-08-14  
Status: accepted  
Owner: repository author

## Context

The first consumer works across repositories that do not share a meaningful
filesystem root. Requiring a project directory would either include unrelated
repositories or force artificial directory moves. Starting in one repository
must remain immediate, while a later TUI session must be able to include another
repository without invalidating its session and worktree identity.

## Decision

A Midgard project is a logical, named set of repository mounts. It has a stable
ID and no filesystem root. Named project records live in the user's Midgard
configuration directory under `projects/<project-id>.json`; each mount contains
a user-facing name and a canonical Git repository path.

An unconfigured repository receives a deterministic implicit project ID. When
the user adds another repository, Midgard can persist that implicit project as a
named project without changing the ID. Existing sessions and worktree ownership
therefore remain valid.

A repository may be mounted by multiple projects. With one match Midgard selects
it automatically. With multiple matches it uses the repository's remembered
choice from local Git config, an explicit `-project`, or a conversational choice
in an interactive terminal. Project and repository mount identities are stored
in session and workspace projections. A session may own one worktree per named
repository mount.

## Consequences

Repositories such as Midgard and Bragi can participate in one project without
making their common `repos` directory a project root. Moving an unrelated parent
directory does not redefine the project, although moved repository paths must be
updated in the catalog.

The catalog and durable identities support the TUI `/repo add` flow. It attaches
the new worktree only at a turn boundary, preserves the state location when an
implicit project becomes named, and qualifies agent tools by repository once a
chat has multiple worktrees. The current kernel remains free of task, planner,
and workflow types.
