# Connect Bots

Connect Bots is an additive product in a cc-connect fork: `bots/` owns the
persistent bot workspace, `cmd/connect-bots/` runs it, and `studio/` supplies the
React client. Existing `web/`, config, engine, and messaging projects are retained.
Only optional native-event/RPC/tool capabilities are added to existing adapters.

Each bot owns one directory, AGENTS.md, `.agents/skills/`, `tmp/`, and durable
conversation history. Shared application instructions and skills live in a
separate `user/` directory. Native Codex/Pi user instructions remain available.
There is one owner per installation, not a new multi-tenant cloud platform.

Connect Bots starts and owns one dedicated Codex app-server. Every product Codex
session, including junior-model maintenance, connects to its concrete endpoint.
The default uses a private Unix socket; Windows uses loopback WebSocket. Native
authentication, user instructions and skills still come from the inherited
Codex home, while SQLite state and logs live under `<data>/codex/`. An explicitly
configured external endpoint must be dedicated to this product; `managed:` and
an implicit shared-daemon fallback are rejected. If the owned child exits, the
HTTP process exits so its supervisor can restart it. Shutdown closes runtime
sessions before stopping the owned app-server.

The coordinator is an ordinary persistent bot with list, send, status, and
create-bot tools. Calls and results stay in the conversation journal. Backend
switching keeps the visible history and explicitly transfers recent conversation
context; Codex and Pi retain their own backend thread IDs.

`bots_publish_files` publishes prepared workspace files as separate assistant
messages with `source: "files"`, `artifact: true` and opaque attachments. Stable
copies remain outside `tmp/`; original paths never enter the public message.

State is written atomically to owner-only JSON files. Conversation events append
to JSONL with monotonically increasing sequence numbers. SSE replays from a
cursor and supports independent browsers. A lost connection never resubmits a
prompt. After a server restart, interrupted work is marked interrupted, rather
than silently repeated.

The runtime retains complete scoped native events alongside legacy normalized
events. Thinking means provider-exposed reasoning summaries. Goals and ultra
come from actual Codex capabilities. Output throughput is labelled an estimate
when the provider does not expose decoder timing; tool execution time is excluded
where item timing is available.

Telegram is an optional surface over the same bot session; it uses cc-connect's
existing platform adapter and dedicated, explicitly enabled tokens supplied by
environment-variable name. Existing Telegram services are not imported or taken
over. Voice is proxied to an OpenAI-compatible Flov transcription endpoint;
browser microphone recording needs HTTPS, and audio upload remains available.

Daily maintenance inventories only each bot's temporary directory using the
configured junior model. Durable instructions, skills, uploads, and artifacts are
outside the cleanup boundary. No general autonomous scheduler or screen streaming
is introduced.

## HTTP contract

All APIs are under `/api/studio`. JSON errors are `{error:string}`. Owner login
uses an HttpOnly cookie; SSE and same-origin requests share it. Mutation requests
check the Origin. The deployment token is read from environment or an owner-only
local file and never returned by the API.

- `GET /health`: unauthenticated liveness.
- `POST /login` `{token}`; `POST /logout`; `GET /session`.
- `GET /bots` -> `{bots:Bot[]}`; `POST /bots` Bot fields -> Bot.
- `GET /bots/:id` -> Bot; `PATCH /bots/:id` partial Bot; `DELETE` archives bot.
- `GET /bots/:id/events?after=seq` -> `{events:Event[]}`.
- `GET /events?after=seq` SSE event `event`, data Event (global sequence).
- `POST /bots/:id/messages` `{text,attachments?}` -> `{turnId}` (202).
- `POST /bots/:id/stop`; `POST /bots/:id/permission` `{requestId,behavior,updatedInput?,message?}`.
- `GET /capabilities?botId=...` -> Capabilities for the selected bot; `GET /bots/:id/goal`; `PUT` native goal fields; `DELETE`.
- `GET /bots/:id/context` -> provider-known usage fields and `compacting`.
- `POST /bots/:id/compact` `{instructions?}` -> `{requestId}` (202); custom instructions are supported by Pi.
- `GET /bots/:id/instructions` -> `{content,path}`; `PUT` `{content}`.
- `GET /user/instructions` and `PUT` same shape.
- `GET /bots/:id/skills` -> `{skills:Skill[]}`; `PATCH` `{disabledSkills:string[]}`.
- `GET /user/skills`; `POST /user/skills` `{name,content}`.
- `POST /bots/:id/skills` `{name,content}`; `GET/PUT /skills/content?path=...`.
- `POST /bots/:id/uploads` multipart `file` -> Attachment; `GET /bots/:id/files/:attachmentId`.
- `POST /transcribe` multipart `file` -> `{text}`.
- `GET /maintenance` -> settings and last reports; `PATCH` settings; `POST /maintenance/run`.

Event types: `message` ({role,content,attachments,source}), `native` (core.NativeEvent),
`agent` (normalized core.Event with error converted to string), `turn`
({status,backend,model,effort,serviceTier,outputTokens,generationMs,tokensPerSecond,error}),
`bot` (updated Bot), `handoff`, `goal_action` (native mutation audit),
`goal_carry` (paused configuration transfer), `maintenance`, `system` ({content}).
Manual compaction uses `compact_action` with one stable request ID; native
compaction events remain available alongside it. Context percentages are shown
only when the runtime reports both usage and a window size.
Every accepted message has a stable product turnId shared by its events; native
thread/turn/item IDs remain in native params. Completed activity collapses in the
client without deleting events. Sequence cursor replay is idempotent.

Goal reads do not append synthetic state to the journal. Responses include the
cursor captured before the native RPC, so newer goal notifications remain
authoritative when the browser merges a response with SSE. Goals set before the
first prompt still materialize and retain their owned thread. A configuration
change transfers an unfinished goal to the replacement thread on pause, with
the remaining budget, and requires explicit resumption. Pending context transfer
is durable across restart. Before resuming the goal, one acknowledged service
turn restores that context while the goal remains paused. Manual compaction
does not create a conversation turn; Stop and shutdown interrupt only its owned
native turn before releasing the busy state.

Defaults: Codex `gpt-6-sol/max`; Pi `deepseek/deepseek-flash/max`; inventory
Codex `gpt-6-luna/max`. Runtime catalogs determine available model efforts.

`Bot.serviceTier` is the selected Codex tier ID, or an empty string for automatic
selection. `Model.serviceTiers` retains each model's advertised IDs, names and
descriptions from `model/list`; `defaultServiceTier` is included when advertised.
No tiers are fabricated for Pi or models with an empty catalog. The selected tier
is sent as `serviceTier` on native thread and turn requests. Returning to automatic
selection explicitly clears a resumed thread's saved override through
`thread/settings/update` with `serviceTier: null`. Model, effort and tier changes
resume the same native conversation; they apply to the next turn and remain in
that turn's journal metadata. Provider authorization and tier pricing remain
properties of the selected runtime and account.
