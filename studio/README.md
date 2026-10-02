# Connect Bots Studio

Architecture and ownership: [ARCHITECTURE.md](ARCHITECTURE.md). Scoped agent
rules: [AGENTS.md](AGENTS.md).

React client for the additive Connect Bots service. Existing cc-connect `web/` stays independent.

```sh
cd studio
pnpm install
pnpm dev
```

Development binds to `0.0.0.0:5173` and proxies `/api/studio` to `127.0.0.1:9830` without rewriting Host, preserving cookie authentication and Origin checks.

For everyday use while source files are being edited, serve a completed build
without hot reload:

```sh
pnpm build
pnpm preview
```

Preview keeps port 5173 and the same API proxy, including SSE and node WebSocket
traffic, but does not watch source files or load Vite's HMR client. Rebuild and
refresh when an update is ready. The full Go product binary embeds this same
build and is the normal deployment path.

```sh
pnpm build
pnpm test
```

Microphone capture requires HTTPS or localhost. Audio-file transcription remains available over LAN HTTP. All conversation data comes from the service; reconnect merges events by sequence and never submits a prompt again. Completed turn activity is retained and collapses for reading.

The compact composer keeps attachment, model and effort, optional voice and send
controls in one toolbar. Click the model name to open the effort slider and
service-tier selector; **Choose model** opens the live Codex/Pi model list. The
status line below the input shows step, turn, tok/s, context, model, effort, tier
and effective session time. **Working** and the turn timer sit directly above
the input. Conversation, activity and menu animations respect the browser's
reduced-motion preference.

Type `$` to filter enabled skills by name or description, then select with
arrows/Enter or a tap. Selections become removable chips and can be sent without
other text. Drafts and selections stay bound to the account, host and bot. Normal
send during an active turn adds the message to the queue above the input;
**Steer** injects a queued message into the current turn. Finished progress and
tool batches fold into one expandable history control before the final answer.

The profile header is replaced by a floating bot card on the right. On screens below 1100 px, the panel button opens the same card as an overlay. Settings, goals, recent requests and published files remain available there; mobile has a separate back button for the roster.

The roster combines the signed-in account's bots across hosts. Each row shows
its device, and the `Server` / `Mac` checkboxes filter visibility. Selecting a
bot opens its workspace without a page reload; the current conversation, files
and editors stay bound to that host. Background host-status and catalog failures
retain the last successful snapshot quietly. Offline hosts keep their known
rows for the current account session and cannot accept new work.

Models and reasoning levels come from the selected bot's live provider catalog. Codex service tiers use each model's advertised IDs, names and descriptions; the selector is absent when no tiers are advertised and no saved override needs resetting. “Auto” clears the saved tier override. Model, effort and tier changes apply to subsequent turns in the same conversation.

Context usage remains unknown until the service reports it; estimates are marked
`≈`. Click **Context** in the status line below the input to compact while idle.
Compaction follows its persisted request ID and native completion events; the
percentage stays visible beside a spinner and send/model gating stays active
until the provider finishes. Bot-published files
render alongside their caption as authenticated image previews or downloadable
document cards.

Current release evidence and remaining native-runtime checks are tracked in
[release readiness](../docs/connect-bots/release-readiness.md).

### Performance and stable local publication

`pnpm bench:render` starts an isolated synthetic render benchmark on loopback
port 5187. See [bench/README.md](bench/README.md) and the
[frontend audit](../docs/connect-bots/frontend-performance.md).

For a preview used throughout the day, publish into a separate directory:

```sh
pnpm deploy:local /absolute/path/to/studio-live
pnpm exec vite preview --outDir /absolute/path/to/studio-live --host 0.0.0.0 --port 5173 --strictPort
```

The publisher keeps previous hashed assets for already-open tabs and replaces
`index.html` last. Subsequent publications do not require restarting preview.
