# Connect Bots

Persistent AI bots with their own workspaces, instructions and skills, brought
together in a responsive React app. A coordinator can delegate to the other bots;
each conversation keeps the actual tool activity, results and files.

Connect Bots lives on the `connect-bots` branch of a
[cc-connect fork](https://github.com/billyhargroveofficial/cc-connect/tree/connect-bots).
The existing cc-connect command and web dashboard remain available. The product
adds `bots/`, `cmd/connect-bots/` and `studio/`, and reuses the Codex, Pi and
Telegram adapters. The original Go module and upstream attribution are retained.

## What you can do

- Create persistent bots with independent folders and a shared coordinator.
- Use Codex through a dedicated app-server, or Pi with a configured DeepSeek provider.
- Change the model, effort and supported Codex service tier from the conversation.
  Codex goals, ultra and subagents appear when the installed runtime supports them.
- See the provider's context usage and start context compaction with its actual
  progress shown in the conversation.
- Inspect searches, tool calls, reasoning summaries and outputs. Completed
  activity folds away and stays available in the journal.
- Edit each bot's `AGENTS.md` and skills, and the installation's shared
  instructions and skills. Native harness user configuration still applies.
- Attach pictures, documents and archives, and let a bot publish its prepared
  files as downloadable conversation cards. Transcribe audio through Flov, and
  optionally connect a bot to Telegram. Telegram and the web app use the same bot conversation.
- Run daily temporary-file inventory with a junior model.

Open “Ещё действия” (⋯) → “Настройки модели” in the composer's toolbar to change
the model, effort or service tier. The tier selector appears when the selected
Codex model advertises tiers in its live catalog; names and descriptions come
from the app-server. “Авто” clears the saved tier override and lets the runtime
choose. These settings apply to subsequent turns in the same conversation.
For manual compaction, open the context indicator in that toolbar and choose
“Сжать контекст” while the bot is idle.

The conversation has no profile header. Bot identity, status, settings, goals,
recent requests and published files live in a floating card on the right.
On narrow screens, open it with the panel button in the upper-right corner;
the upper-left arrow returns to the bot list on mobile.

One installation belongs to one owner. Bots run with that owner's local harness
credentials and filesystem access. No screen streaming or cloud control plane is
required.

## Build and run

Install Go 1.25 or newer, Node.js 22.18 or newer, and pnpm 10 to build. Default
startup requires an installed Codex CLI; authenticate it as the owner. Install Pi
and configure its provider credentials for Pi bots.

```sh
git clone --branch connect-bots https://github.com/billyhargroveofficial/cc-connect.git
cd cc-connect
make -f Makefile.connect-bots build
./bin/connect-bots --addr 127.0.0.1:9830
```

Open `http://localhost:9830`. The server stores data outside the checkout, under
`~/.local/share/connect-bots` by default, or `$XDG_DATA_HOME/connect-bots` when
that variable is set. Read `<data-directory>/token` locally and enter it in the
login screen; keep it out of URLs, screenshots and commits. See
[Deployment](DEPLOYMENT.md) for LAN access and services.

The built binary embeds the web app. Node.js is needed for building the frontend
and for a Pi runtime; the Go web server itself does not need Node.js.

Connect Bots starts one dedicated Codex app-server for all its Codex bots and
maintenance. It uses the owner's existing Codex authentication, instructions and
skills, with private SQLite state and logs under the product data directory.
Connect Bots owns that process and stops it during shutdown. See
[deployment options](DEPLOYMENT.md#codex-runtime) for an external dedicated endpoint.

## Workspaces and continuity

Every bot has an `AGENTS.md`, `.agents/skills/` and `tmp/` inside its own
workspace. Shared product instructions live in `user/AGENTS.md` and shared
product skills in `user/skills/`. Turning off a skill for a bot does not rewrite
the owner's global harness configuration.

The bot's skill list follows its selected backend: native Codex and Pi folders
appear only for that harness, while product and project skills remain available
to both. The global editor includes both native catalogs. Plugin-provided skills
still follow the native harness configuration; their cache is not managed by
this editor.

Switching between Codex and Pi retains the visible product journal and transfers
recent conversation context. Each harness keeps its own native thread ID; it
does not gain the other harness's hidden state. The selected model, effort and
supported service tier apply to the next turn.

Output tokens per second are an estimate where the provider does not report
generation timing. Thinking blocks contain reasoning exposed by the provider.
The interface does not manufacture either value.

Prepared files are published through `bots_publish_files`. The tool copies up to
10 files from the bot's workspace into durable attachment storage, so downloads
survive temporary-file cleanup. Files are limited to 25 MiB each. Telegram-origin
work can return the same files to that conversation; a web conversation does not
guess a Telegram recipient.

Temporary-file maintenance is confined to each bot's `tmp/`. Instructions,
skills, uploads, conversation history and durable artifacts are outside that
cleanup boundary. Maintenance settings and reports are available in the app.

## Development

Run the API and frontend in separate terminals:

```sh
make -f Makefile.connect-bots dev-api
```

```sh
make -f Makefile.connect-bots dev-studio
```

The frontend listens on `0.0.0.0:5173` and proxies `/api/studio` to
`127.0.0.1:9830`. Use `http://<computer-LAN-IP>:5173` from another device on the
same network. Microphone recording requires a secure browser context; audio-file
upload also works over LAN HTTP.

```sh
make -f Makefile.connect-bots check
```

This builds the frontend, runs its unit tests, vets all Go packages with
`no_web`, and runs the product, core, Codex, Pi and command tests with the race
detector. Core critical-user-journey tests are included. The old web dashboard's
generated assets are not needed.

The optional `make -f Makefile.connect-bots check-upstream` runs all Go packages
with the existing upstream CI gates for live Cursor probes. Some legacy tests
also need a local Claude executable. See [validation notes](validation.md) for
the checked environment and explicit exceptions. To check the old dashboard
too, build `web/` first and run the repository's ordinary checks.

Before release, also test login, reconnect, interrupted turns, model switching,
attachments and the mobile layout with real runtimes. Adapter tests cover the
protocol boundaries without requiring live credentials.

## Keeping upstream

Follow [the repository development rules](../../AGENTS.md). Keep product code in
its own directories and add adapter behavior through optional capability
interfaces. The old cc-connect configuration remains separate from Connect Bots
state.

To bring in upstream changes, fetch `upstream` and merge the intended upstream
branch into the product branch. Review adapter seams and run the release checks
after resolving conflicts. Never commit runtime data or provider credentials.

The [architecture and HTTP contract](architecture.md) describe the product's
internal boundaries.
