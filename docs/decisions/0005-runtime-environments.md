# Decision: reusable runtime environments

Date: 2026-08-14  
Status: accepted  
Owner: repository author

## Context

The repository owner reuses environment configuration across side projects and
often retains production credentials as those projects grow. Per-repository
`.env` files duplicate configuration, make variable discovery slow for agents,
and leave secret lifecycle outside Midgard's existing OS-keyring boundary.

## Decision

Midgard will manage named, reusable runtime environments. Plain environment
values and agent-visible descriptions live in Midgard's central configuration.
Secret values live only in the OS keyring; catalog revisions contain opaque
keyring references. A project remembers one selected environment, and an
environment may extend one parent whose current revision is included when a turn
begins.

Each turn resolves an immutable environment snapshot. Shell and check actions
commit that snapshot ID before dispatch. The workspace runner rejects a
different revision, retrieves secret bytes only after durable dispatch, and
injects values only into the child process. Provider requests receive variable
names, kinds, descriptions, and source revision metadata, never values. Midgard
provides no command that reveals a secret value.

Known secret values are redacted from command output before the result becomes
durable. This protects against accidental echoing, not deliberate exfiltration:
an agent-controlled process with an injected credential can transform, write,
or transmit it. Production credentials therefore remain within the accepted
private trusted-agent prototype boundary.

## Consequences

- Projects can share configuration without shared filesystem roots or `.env`
  files.
- Environment selection changes only at an idle turn boundary and affects later
  turns.
- Secret rotation creates a new immutable environment revision and keyring
  reference; prior revisions remain reproducible.
- A missing keyring value stops the affected action rather than running with a
  partial environment.
- Arbitrary typed application configuration, multiple-parent inheritance, and
  protection from a malicious injected process are not part of this decision.

## Verification and revisit

Tests must prove revision fencing, parent overrides and their provenance,
project binding, child-only injection, missing-secret failure, agent metadata without values, output
redaction, and absence of known secret bytes from events, artifacts, and config.
Stop the prototype if Midgard records injected secret bytes. Revisit the trusted
agent assumption before a second user, unattended execution, or production-grade
containment.
