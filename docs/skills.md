# Agent skills

Midgard exposes installed coding-agent skills through the feature-delivery
policy. The system prompt advertises the bounded `skill_search` action instead
of embedding every skill name and description. A model searches for a relevant
available skill, then loads its instructions with a committed
`skill_read` action, so skill use has the same durable intent, validation,
commit, dispatch, and result evidence as other host actions.

Midgard scans these sources in precedence order; the first skill name wins:

1. Project: `.midgard/skills`, `.agents/skills`, `.github/skills`,
   `.claude/skills`, then the repository's legacy `skills` directory.
2. Personal: Midgard's OS config directory (`midgard/skills`),
   `~/.agents/skills`, `~/.copilot/skills`, `~/.claude/skills`, then
   `~/.codex/skills` as a compatibility source.

Project skills therefore override personal skills without making Midgard depend
on another agent. `.agents/skills` is the preferred portable location. The
catalog metadata and allowlisted locations are built once when Midgard starts.
`skill_read` opens allowlisted content on demand, so body edits are visible
immediately; adding or removing a skill or changing its frontmatter requires a
restart. Discovery is bounded to six directory levels and 2,000
directories per source, ignores `.git`, `node_modules`, and symlinks, and never
lets a model supply a filesystem path.

Retrieval is deliberately progressive:

- `skill_search(query)` searches the available catalog by name, group, and
  description and returns at most eight short matches. It never returns skill
  bodies and respects project and group masks.
- `{"name":"heimdal"}` reads the complete `SKILL.md`. Primary
  instructions are capped at 24 KiB and cannot be sliced.
- `{"name":"heimdal","query":"visual evidence"}` searches
  reference files and returns at most eight bounded, line-numbered excerpts.
  Search is navigation only: zero matches are not evidence that a requirement
  is absent.
- Adding `"resource":"references/example.md"` narrows that search to one
  allowlisted resource.
- After search, `resource`, `start_line`, and `line_count` retrieve a bounded
  section. Reference reads are limited to 120 lines and 16 KiB of output;
  whole-reference reads are rejected.

If `SKILL.md` requires a reference to be read completely, the model pages it
with consecutive bounded ranges until the result reports `has_more: false`.
Optional or broad references may be searched first and narrowed to the relevant
line ranges. Every search reports navigation mode and whether its match set was
truncated.

Resources resolve relative to the installed skill directory. Absolute paths,
parent traversal, and symlink escapes are rejected. The catalog is discovered
before the runtime starts, so model-authored paths cannot expand its authority.

## Project availability

All discovered skills are available by default. `/skills` opens the effective
catalog for the current project. Type to filter by name or description, use
Up/Down to select, and press Space to toggle availability. Changes are accepted
only between turns and apply to later turns.

Masks are stored by stable project ID in Midgard's OS config directory, separate
from the installed skill and repository. A mask never edits or deletes skill
content. It filters `skill_search` and `skill_read`, so a hidden skill cannot be
selected by name even if the model remembers it from an earlier turn.

Technology-specific skills may be assigned to one manually managed group. Keep
general, technology-agnostic skills ungrouped. Group metadata lives in
`skill-groups.json` under Midgard's OS config directory and can be managed with:

```sh
midgard skills group set xtdb xtdb-query-and-transact
midgard skills group set spacetimedb cli concepts rust-server typescript-client typescript-server void-spacetimedb
midgard skills groups
midgard skills group disable xtdb
```

The `/skills` picker renders a group row above its indented members. Space on a
group toggles the whole group for the current project. A disabled group removes
all its members from both `skill_search` and `skill_read`; individual skill
masks remain available for finer exceptions.
