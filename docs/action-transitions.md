# Action state transitions

| From | Event | To | Preconditions |
|---|---|---|---|
| absent | `action.intent` | intent | version 1, bounded arguments |
| intent/validated/approval_pending | `action.intent_revised` | intent | uncommitted, next version |
| intent/validated/approval_pending | `action.retracted` | retracted | uncommitted |
| intent | `action.validated` | validated | capability and schema accepted for exact version |
| validated | `action.approval_requested` | approval_pending | policy requires approval |
| approval_pending | `action.approved` | validated | human/server decision recorded |
| approval_pending | `action.rejected` | rejected | human/server decision recorded |
| validated | `action.committed` | committed | exact version, unique commit and idempotency keys |
| committed | `action.dispatched` | dispatched | durable worker owner and fence |
| dispatched | `action.succeeded`/`action.failed` | succeeded/failed | same owner and fence |
| failed | `action.compensation_committed` | compensation_committed | new durable compensation action |

No repair or retract event is legal after commit. External effects are not
claimed exactly once: the kernel guarantees durable at-most-once dispatch for a
commit ID, while tools must provide idempotency or compensation.

`action.committed` is also rejected while an unacknowledged `control.steer`
exists for the session. An action committed before steering was queued remains
authoritative and may finish; only uncommitted intent can be superseded.
