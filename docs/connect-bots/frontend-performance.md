# Frontend render audit — 2026-10-02

Scope: the React Studio used on port 5173. This audit covers the workspace data
path, every primary UI surface, history projection, Markdown, animation context,
scrolling, and local deployment. The upstream legacy `web` application is not
loaded by Connect Bots Studio and was not changed.

## Findings and changes

| Area | Finding | Change / verified behavior |
| --- | --- | --- |
| SSE / workspace | A token dispatched a workspace update and copied its bot journal on every event. | Native deltas (including command output and Pi message updates) are batched for at most 32 ms. Completion, questions, errors and other non-delta events flush in order immediately. No events are discarded. Account/node disposal cancels old batches. |
| React ownership | Even a referentially stable batch still woke the root `App` because journals lived in its reducer. | Journals now use per-bot `useSyncExternalStore` subscriptions. Selected-bot tokens render only the conversation; background-bot tokens produce no React commit. The roster has a separate message/system subscription. |
| Snapshots / reconnect | Identical replay replaced event and bot objects. | Equal replay retains references; changed or out-of-order snapshots still merge correctly. |
| Chat switching | Returning to a chat requested its complete history again. | One successful history request per account/node workspace; concurrent loads share a promise. Explicit refresh bypasses it; failures are retryable. |
| Workspace / routing | Inline callbacks and fresh empty arrays invalidated child memo boundaries. | Stable callbacks and empty values; background bot events leave the active chat props unchanged. Route, account and node identity checks remain. |
| Roster | Token updates rescanned all event histories, recreated catalogs and redrew rows. | Separate message/system preview projection, memoized catalogs/rows, reverse search without copying the journal. Filtering and selection layout animations remain. |
| Transcript reducer | Every incoming event rebuilt and finalized every previous turn. | Conversation-scoped projection cache reuses unchanged turns. Corrected snapshots, late root metadata, shared decisions and scope changes invalidate affected state. All semantic fixtures compare cached and complete replay. |
| Old answers / Markdown | Old Markdown, math and code highlighting reran on each token. | Memoized turns and Markdown. Active streaming text uses a lightweight pre-wrapped renderer with the same shimmer; Markdown is parsed once when the item completes. Raw payloads remain lazy behind disclosures. |
| First long-history frame | Parsing every old Markdown answer delayed the composer and latest messages. | Transcript is its own lazy chunk. The latest 12 turns mount first and older turns hydrate in bounded idle batches; once caught up, future turns remain synchronous. |
| Framer Motion | Presence layout context updates crossed memo boundaries even when items stayed in place. | Layout propagation is disabled on non-layout presence wrappers. Enter/exit and disclosure animation, reduced motion and accessibility remain. Roster/island layout animations keep their own boundaries. |
| Scroll | Every event read `scrollHeight` in a layout effect, forcing layout even for unchanged geometry. | Initial positioning plus ResizeObserver on actual size changes; fallback remains for environments without ResizeObserver. Reading older history stays in place. Completed offscreen turns use CSS content visibility with remembered intrinsic size. |
| Composer / voice / attachments | Streaming refreshed the composer through callback/context object churn. | Stable context and action props, memoized composer. Typing, upload, recording and send state remain local. |
| Bot island / profile | Island sorted the whole journal on every token. | Stable message-only input and a memoized island. Profile/instructions/skills still update on real bot changes. |
| Global settings / new bot | Open dialogs were rerendered by unrelated workspace events. | Memoized lazy surfaces with stable creation, enrollment, deletion and close callbacks. |
| Model / effort / tier / context / goal | Their updates are semantic and much less frequent than token events. | Existing scope/lifecycle logic retained; context return value stabilized. Model settings and context controls are isolated by the composer boundary. |
| Login / theme / resize / editors | Work is local to interaction or account/media changes. | Retained existing local state, focus handling and pointer lifecycle. Editors only parse Markdown in preview mode; no blanket memoization added. |
| Deployment | Preview served the directory Vite clears during a build; old lazy chunks could disappear from open tabs. | Publish assets to a separate live directory, preserve prior hashed assets and replace the index last. Source edits/builds do not reload clients. |

## Evidence

The synthetic development fixture contains 100 completed turns, over 3,500
journal events, formatted Markdown/code/math, and one live commentary item.
The same existing Chrome was used, without touching user chats or sending
requests to real bots.

- Before changes, five synchronous token updates took **489–758 ms**, median
  **630 ms**. After the first projection/memo/presence changes, typical updates
  were **35–45 ms**; timings vary with warmup and concurrent machine load.
- Render counters after warmup: ten token updates caused ten live-turn and ten
  Markdown renders. Completed turns, composer and island rendered **zero** times.
- In the full-workspace fixture, a burst of **100** tokens caused **one** live-turn
  render and **zero** Markdown renders (median about **9 ms** React render time
  across the final repeated run). `App`, workspace shell and roster rendered
  **zero** times. Background-bot tokens caused **zero React commits**.
- Typing into the composer caused one composer render and no history render.
  An idle node-poll cycle caused no heavy-surface render.
- Switching between two bots twice requested each history only once. The
  narrow-viewport check preserved typing and response-detail expansion without
  horizontal page overflow.
- While reading older history at scroll offset 1,000 px, a 100-token burst left
  the offset at 1,000 px.
- A local read-only projection benchmark on the four largest real journals
  (2,056–6,376 events) reused all unchanged turns. Warm cached projection took
  approximately **0.4–2.7 ms**, versus **2.4–9.4 ms** for complete replay. Only
  sizes/timings were recorded; conversation content was not published.

Run the repeatable browser fixture with `pnpm --dir studio bench:render`.
See [the benchmark instructions](../../studio/bench/README.md). Counters and
Profiler instrumentation exist only in that fixture, not in production.

## Checks and limits

All 235 frontend regression tests, TypeScript and production build pass. Regressions
cover snapshot identity, batching/order/cancellation, chat-history request reuse,
callback stability, and retaining old lazy chunks during publication. Existing
account isolation, Codex/Pi replay, subagents, permissions, files, math, theme,
focus and reduced-motion tests remain in the gate.

The first opening of a long history still parses and mounts its contents; this
change does not add pagination or remove older messages. The current turn is
reprojected on each published batch, while unchanged turns are reused. Extremely
large individual turns may justify an incremental active-turn reducer later.
Browser timing evidence here is Chrome development-mode evidence, not a measured
Safari/mobile-device latency guarantee.
