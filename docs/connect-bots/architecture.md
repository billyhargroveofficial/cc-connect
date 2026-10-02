# Connect Bots

The detailed Studio ownership and hot-path map lives in
[`studio/ARCHITECTURE.md`](../../studio/ARCHITECTURE.md). The application shell
composes chat, transcript and settings capsules; high-frequency journals use
per-bot external-store selectors instead of a global React broadcast.

Connect Bots is an additive product in a cc-connect fork: `bots/` owns the
persistent bot workspace, `cmd/connect-bots/` runs the account hub,
`cmd/connect-bots-node/` runs a paired computer, and `studio/` supplies the React
client. Existing `web/`, config, engine, and messaging projects are retained.
Only optional native-event/RPC/tool capabilities are added to existing adapters.

Each bot owns one directory, AGENTS.md, `.agents/skills/`, `tmp/`, and durable
conversation history. Shared application instructions and skills live in a
separate `user/` directory within its account. The host account router resolves
the signed-in account to a separate Store, Workspace, Runtime, maintenance worker
and Telegram manager. Bot IDs, files, event cursors and tools are scoped through
that workspace; a browser cannot select a different tenant with a request field.

The owner's workspace remains at the original data root. Additional accounts use
`<data>/users/user_<random>/`; usernames never form filesystem paths. The owner
retains its native Codex/Pi skills and environment-backed Telegram connections.
Other accounts have private product skills and Pi configuration, and their
editors do not expose the owner's native catalogs. Startup preloads persisted
accounts so their Telegram connections and maintenance resume without a browser
login.

Connect Bots starts and owns one dedicated Codex app-server shared by the host's
accounts. Every product Codex session, including junior-model maintenance,
connects to its concrete endpoint.
The default uses a private Unix socket; Windows uses loopback WebSocket. Native
authentication, user instructions and skills still come from the inherited
Codex home, while SQLite state and logs live under `<data>/codex/`. An explicitly
configured external endpoint must be dedicated to this product; `managed:` and
an implicit shared-daemon fallback are rejected. If the owned child exits, the
HTTP process exits so its supervisor can restart it. Shutdown closes runtime
sessions before stopping the owned app-server.

Account boundaries protect the browser/API data model, not arbitrary agent shell
commands. Harnesses run as the same Unix user with the host's Codex/provider
credentials and filesystem permissions. The shared Codex server also retains its
native host configuration. This deployment model assumes trusted users; it does
not provide separate OS sandboxes or per-account inference credentials.

The coordinator is an ordinary persistent bot with list, send, status, and
create-bot tools. Calls and results stay in the conversation journal. Backend
switching keeps the visible history and explicitly transfers recent conversation
context; Codex and Pi retain their own backend thread IDs.

`bots_publish_files` publishes prepared workspace files as separate assistant
messages with `source: "files"`, `artifact: true` and opaque attachments. Stable
copies remain outside `tmp/`; original paths never enter the public message.

State is written atomically to owner-only JSON files. Conversation events append
to JSONL with monotonically increasing sequence numbers. SSE replays from a
workspace cursor and supports independent browsers. A lost connection never
resubmits a prompt. After a server restart, interrupted work is marked
interrupted, rather than silently repeated.

The runtime retains complete scoped native events alongside legacy normalized
events. Thinking means provider-exposed reasoning summaries. Goals and ultra
come from actual Codex capabilities. Output throughput is labelled an estimate
when the provider does not expose decoder timing; tool execution time is excluded
where item timing is available.

Telegram is an optional surface over the same bot session; it uses cc-connect's
existing platform adapter and dedicated, explicitly enabled tokens supplied by
environment-variable name for the owner. Additional accounts cannot read the
host's environment-backed Telegram tokens. Existing Telegram services are not
imported or taken over. Voice is proxied to an OpenAI-compatible Flov transcription
endpoint; browser microphone recording needs HTTPS, and audio upload remains
available.

Daily maintenance inventories only each bot's temporary directory using the
configured junior model. Durable instructions, skills, uploads, and artifacts are
outside the cleanup boundary. No general autonomous scheduler or screen streaming
is introduced.

## Paired hosts

A remote node belongs to exactly one hub account. Pairing uses a short-lived,
single-use code issued to that account and exchanges it for a private node
credential. The node initiates the connection to the hub, so the hub does not
connect to a user-supplied host address or require inbound access to a laptop.
TLS is required beyond loopback by default; plaintext LAN use requires explicit opt-in on the
hub and during node pairing. Node credentials are independent from browser
sessions and Codex authentication. Removing a host revokes its node connection.

The node owns a local Store, Workspace, Runtime, maintenance worker and dedicated
Codex app-server. Bot files, journals, uploads and Codex state remain on that
computer. Native Codex authentication, user instructions and skills come from
the local user or an explicitly configured local Codex home. The hub routes
authenticated browser requests and streaming responses to that node; it never
copies the hub owner's harness credentials into the node workspace. The node
command has no Studio dependency and can be cross-compiled with CGO disabled.

The roster is an account-scoped catalog combining bots from all paired hosts and
the hub. Each row has a device label, and **Server** / **Mac** checkboxes filter
visibility. Bot identity in this catalog is the pair `(nodeId, botId)`, so equal
bot IDs from different workspaces cannot collide. Last successful catalogs remain
in account-local browser memory while a host is offline; sign-out and account
changes clear them, and removing a host drops its catalog.

The browser opens one host's conversation at a time. Clicking a roster bot
switches that active workspace without navigating or reloading the document.
`X-Connect-Bots-Node` binds ordinary
workspace requests to a node; EventSource and direct GET/HEAD requests use the
`node` query parameter. An absent node binding retains the local hub workspace
for compatibility. The selected node must belong to the session account.
Account binding is checked independently, before dispatch, and a node selection
does not grant access to another account. The client resets workspace state and
event cursors on host changes; each host has its own journal sequence.

Only the active workspace has an event stream. Other online hosts refresh their
bot catalogs through requests with explicit account and node bindings. Host
polling runs in the background; transient status or catalog failures retain the
last successful snapshot without a repeating error toast. Expired sessions and
failed user operations still report their state. The built app and preview mode
do not inject a hot-reload client; Vite dev mode remains a development tool.

A bot belongs to the host where it was created. Switching the active host does
not change that bot's execution location or replay an in-flight prompt. An offline
host cannot accept new operations. Reconnection resumes access to its durable
local workspace. Version one has no cross-host bot migration or delegation;
coordinator tools resolve only bots in their current workspace.

`--node-binaries` enables account-authenticated downloads of the two fixed macOS
binary filenames. It is not a general static directory server. `--public-url`
controls the externally advertised pairing origin behind a reverse proxy.

## HTTP contract

All APIs are under `/api/studio`. JSON errors are `{error:string}`. Account login
uses an opaque HttpOnly, SameSite Strict cookie with a 30-day absolute lifetime;
HTTPS adds Secure. Only a SHA-256 session digest is persisted, and logout revokes
the session and closes its active requests, including SSE. Mutation requests
check the Origin. Login and registration have rate and KDF concurrency limits.

`auth/accounts.json` stores accounts, bcrypt password hashes (cost 12 over a
SHA-256 password digest) and session digests, with permissions `0600` in a `0700`
directory. Usernames are normalized lowercase ASCII, 3–32 characters, using
letters, digits, `.`, `_` or `-` and starting with a letter or digit; passwords are
8–128 bytes and are not trimmed.
The client clears workspace state and event cursors when the account changes.

Protected workspace requests must send `X-Connect-Bots-Account` with the signed-in
user ID. EventSource and direct file GETs instead pass `expectedAccount` in the
query string. The binding must match the session account; a missing or mismatched
binding returns HTTP 409 with `code: "account_changed"`. Logout is bound as well,
so an old tab cannot mutate or sign out a newly selected account using its shared
browser cookie.

On a fresh installation the first registration claims the root workspace. An
upgraded workspace requires a valid legacy owner cookie or a one-time
`accessKey` on registration to claim that root. Anonymous registrations on an
upgraded host receive a new workspace. `auth/workspace.json` persists this policy
across restarts. The legacy token is never normal account authentication and is
never returned by the API.

- `GET /health`: unauthenticated liveness.
- `POST /login` `{username,password}`; `POST /register` `{username,password,accessKey?}`; `POST /logout`.
- `GET /session` -> `{authenticated,user?:{id,username},registrationAllowed,setupRequired?,legacyClaimAvailable?}`.
- `GET /nodes` -> `{nodes:NodeInfo[]}`; `POST /nodes/enrollments` `{name}` -> one-time pairing details; `DELETE /nodes/:id` revokes a host.
- `GET /nodes/binary/darwin/arm64` and `/nodes/binary/darwin/amd64` -> fixed configured node binary, with account authentication.
- `POST /nodes/enroll` exchanges a valid pairing code; `GET /nodes/connect` upgrades the authenticated node connection to WebSocket. These use pairing/node credentials instead of a browser session.
- `GET /bots` -> `{bots:Bot[]}`; `POST /bots` Bot fields -> Bot.
- `GET /bots/:id` -> Bot; `PATCH /bots/:id` partial Bot; `DELETE` archives bot.
- `GET /bots/:id/events?after=seq` -> `{events:Event[]}`.
- `GET /events?after=seq` SSE event `event`, data Event (account workspace sequence).
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

Every protected route dispatches to the workspace from the authenticated account,
and its selected host, including capabilities, file downloads and SSE. The loopback-only Pi bridge at
`POST /internal/tools` uses a separate random internal token per workspace, never
a browser credential; its token selects the workspace before resolving a bot ID.
Node administration and enrollment remain on the hub even when a remote node
binding accompanies the request; these routes are never proxied into a workspace.

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
