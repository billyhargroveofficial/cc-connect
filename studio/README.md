# Connect Bots Studio

React client for the additive Connect Bots service. Existing cc-connect `web/` stays independent.

```sh
cd studio
pnpm install
pnpm dev
```

Development binds to `0.0.0.0:5173` and proxies `/api/studio` to `127.0.0.1:9830` without rewriting Host, preserving cookie authentication and Origin checks.

```sh
pnpm build
pnpm test
```

Microphone capture requires HTTPS or localhost. Audio-file transcription remains available over LAN HTTP. All conversation data comes from the service; reconnect merges events by sequence and never submits a prompt again. Completed turn activity is retained and collapses for reading.

The compact composer keeps attachment, voice, context, overflow and send controls in one toolbar. Open “More actions” (⋯) → “Model settings” to change the model, effort or service tier. For manual compaction, open the context indicator and choose “Compact context” while the bot is idle. Conversation, activity and menu animations respect the browser's reduced-motion preference.

The profile header is replaced by a floating bot card on the right. On screens below 1100 px, the panel button opens the same card as an overlay. Settings, goals, recent requests and published files remain available there; mobile has a separate back button for the roster.

Models and reasoning levels come from the selected bot's live provider catalog. Codex service tiers use each model's advertised IDs, names and descriptions; the selector is absent when no tiers are advertised and no saved override needs resetting. “Auto” clears the saved tier override. Model, effort and tier changes apply to subsequent turns in the same conversation.

Context usage remains unknown until the service reports it; estimates are marked `≈`. Manual compaction follows its persisted request ID and native completion events. Bot-published files render alongside their caption as authenticated image previews or downloadable document cards.
