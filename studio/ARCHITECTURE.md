# Studio architecture

Connect Bots Studio is one product domain with a thin application shell and
three feature capsules. The server is authoritative, ordinary UI state is
local, and the high-frequency event journal has its own subscription store.

## Ownership

| Area | Path | Owns |
| --- | --- | --- |
| Composition shell | `src/App.tsx`, `src/main.tsx` | Account route, workspace composition, lazy feature boundaries |
| Workspace runtime | `src/hooks/useWorkspace.ts` | Authentication, host selection, SSE lifecycle, catalogs and capabilities |
| Event projection | `src/lib/eventJournal.ts`, `src/hooks/useEventJournal.ts` | Per-bot journals and roster-only previews |
| Chat | `src/features/chat/` | Conversation layout, composer, model/context/goal controls and bot island |
| Transcript | `src/features/transcript/` | Provider event normalization, turn projection and rendering |
| Settings | `src/features/settings/` | Bot, account, skill, maintenance and host editing |
| Shared UI/runtime | `src/components/`, `src/lib/` | Reusable controls, identity, motion, API and theme primitives |

Dependencies point from the shell into features and shared code. Features may
use shared hooks, components and libraries. Shared code does not import a
feature. The bot settings panel embedded by the bot island is an intentional
public surface of the settings capsule.

## Streaming path

```text
EventSource → 32 ms delta batch → EventJournal
                                  ├─ selected bot → ConversationPane → ChatRoom → Transcript
                                  └─ message/system → RosterRegion → BotRoster

bot snapshot → workspace reducer → shell/catalog/status update
```

`EventJournal` uses `useSyncExternalStore` subscriptions. A token for another
bot produces no React commit. A token for the selected bot changes only its
conversation projection. Roster previews subscribe to message/system events
and remain stable for thinking, tools and token deltas.

Transcript projection retains completed turn references. The transcript is a
separate lazy chunk because Markdown, highlighting and math are the largest UI
dependency. A long history mounts the latest 12 turns first and hydrates older
turns in idle batches; after it catches up, new turns render synchronously.

## State rules

- Server/account/host lifecycle: `useWorkspace`.
- High-frequency immutable snapshots: `EventJournal` selectors.
- Interaction state: nearest component.
- Durable user preference: a small dedicated hook plus local storage.
- Derived collections: memoize at the owning boundary and retain references
  when their content is equal.

Do not introduce one global Context/provider for workspace data. It would make
every token a broadcast. Add a narrow store selector only when more than one
mounted consumer needs the same high-frequency projection.

## Verification

`pnpm test` is the functional gate and `pnpm build` validates chunk boundaries.
`pnpm bench:render` serves an isolated 100-turn/3,500-event fixture on port
5187 with render counters. It never talks to real bots. Commands live in
`bench/README.md`; measured findings live in
`../docs/connect-bots/frontend-performance.md`.
