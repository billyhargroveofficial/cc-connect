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

Open “Ещё действия” (⋯) → “Модель и рассуждение” beneath the composer to change the model or effort. For manual compaction, open the context indicator and choose “Сжать контекст” while the bot is idle.

Models and reasoning levels come from the selected bot's live provider catalog. Context usage remains unknown until the service reports it; estimates are marked `≈`. Manual compaction follows its persisted request ID and native completion events. Bot-published files render alongside their caption as authenticated image previews or downloadable document cards.
