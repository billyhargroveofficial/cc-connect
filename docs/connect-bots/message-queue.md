# Owner message queue and steering

An owner message submitted while a bot is working is accepted into that bot's
FIFO queue. The current final answer is committed before the next queued turn
starts. Queue IDs are reserved future product turn IDs, so `WaitTurn` also works
for messages submitted through Telegram. Internal bot delegation keeps its busy
rejection and cycle checks instead of waiting on a queued active dependency.

All routes are relative to `/api/studio` and use the existing account and node
selection boundary:

| Route | Request / response |
| --- | --- |
| `POST /bots/:id/messages` | `{text, attachments?, mode?: "queue" \| "steer"}` → `{turnId, status: "running" \| "queued" \| "steered", queueId?}` (202) |
| `GET /bots/:id/queue` | `{messages: [{id,text,attachments?,source,createdAt,status:"queued"}], paused}` |
| `DELETE /bots/:id/queue/:messageId` | Remove only an awaiting message; never interrupts the active turn. |
| `POST /bots/:id/queue/:messageId/steer` | Inject an awaiting message into the active turn; return a `steered` receipt. |
| `POST /bots/:id/queue/resume` | Resume a paused queue (202). |

Empty `mode` and `queue` use the same normal send behavior: start immediately
when idle or enqueue when active. `steer` requires an active native turn. Codex
uses `turn/steer` with the owned thread and `expectedTurnId` precondition; Pi uses
its native `steer` command. Neither operation interrupts and recreates the turn.
Unsupported, starting, or completed sessions return 409. A provider rejection
keeps the queued message available. Images stay native image inputs and files
remain references to workspace-owned upload paths.

`queue` SSE records contain the item fields plus `count`, `paused`, optional
`error`, and status `queued`, `started`, `cancelled`, `steered`, `failed`, or
`uncertain`.
`queue_control` records contain `paused`, `reason`, and optional `error`. Both
types have an empty outer `turnId` and must not create conversation rows. The
queue GET gives the authoritative initial snapshot; live changes use the same
journal stream as the conversation.

Stop, a failed/interrupted turn, and shutdown pause pending work. Queue records
survive restart, but recovered work waits for explicit resumption or a new
normal owner message. A started message is never automatically replayed because
its provider may have received it before the interruption. A durable
`queue_steer` intent also prevents replay if the service dies after sending a
steer but before persisting its acknowledgement. A timeout, cancellation,
transport failure, or malformed acknowledgement cannot prove rejection: that
item becomes `uncertain`, is removed from automatic dispatch, and pauses other
awaiting work. Only an explicit negative provider acknowledgement restores an
actionable queue item. Once the provider acknowledges steering, the message is
consumed even if the subsequent transcript write fails; failures are logged
without inviting duplicate submission. If Pi settles while a steer is in
flight, its dedicated native steering queue is cleared and delivery remains
uncertain so a later turn cannot accidentally receive that stale instruction.

The queue is bounded to 64 messages per bot. Existing message and attachment
limits still apply. Manual compaction keeps its existing busy gate.
