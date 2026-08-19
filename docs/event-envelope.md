# Canonical event envelope

SQLite assigns one authoritative, gap-free sequence per session at commit.
Payloads are bounded JSON values; large or complete native streams are immutable
artifacts. Event IDs are supplied by the caller for deduplication.

| Field | Rule |
|---|---|
| `event_id` | non-empty globally unique string |
| `session_id` | non-empty owner of ordering |
| `sequence` | positive, assigned by SQLite |
| `turn_id` | optional bounded turn identifier |
| `actor` | `user`, `model`, `server`, `tool`, or `policy` |
| `kind` | dotted lower-case semantic name |
| `schema_version` | positive integer |
| `causation_id` | optional source event/action identifier |
| `correlation_id` | optional cross-event operation identifier |
| `visibility` | `public`, `internal`, or `secret` |
| `payload_json` | optional valid JSON, at most 64 KiB |
| `artifact_ref` | optional immutable `sha256:<hex>` reference |
| `created_at` | server timestamp in UTC |

Provider token deltas are not canonical rows. Normalizers append useful
boundaries such as item start/finish, tool intent, usage, errors, and completion.
Unsupported observations remain recoverable from the raw trace artifact.

