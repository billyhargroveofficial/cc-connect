# Connect Bots Studio

Read `ARCHITECTURE.md` before changing frontend ownership or the streaming
path. The repository root `AGENTS.md` still applies.

- `App.tsx` is a composition shell. Bot behavior belongs in `features/*`,
  workspace transport/lifecycle in `hooks/*`, and domain-neutral primitives in
  `lib/*` or `components/*`.
- Never put token journals into Context or top-level React state. Subscribe to
  `EventJournal` by bot or roster projection so unrelated streams cannot wake
  the shell.
- Preserve object identity for unchanged bots, events and transcript turns.
  Do not clone or sort full journals in a render path.
- Heavy history, Markdown, math and syntax highlighting stay behind the lazy
  transcript boundary. Long histories must remain progressively mounted.
- Animations may express an actual UI transition; token updates and scroll
  containers must not participate in layout projection.
- Every frontend bug fix needs a regression test. Streaming/render changes run
  `pnpm test`, `pnpm build`, and the focused `pnpm bench:render` browser smoke.
