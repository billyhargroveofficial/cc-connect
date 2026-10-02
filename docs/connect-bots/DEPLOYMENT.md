# Deployment

Connect Bots is a Go HTTP server with an embedded React app, local bot
workspaces, username/password accounts and a dedicated Codex app-server child.
An account can pair remote nodes whose workspaces and Codex run on their own host.
Default startup requires Codex CLI on `PATH`. Run it as the host owner with
authenticated Codex; Pi is optional.
Harness executables, credentials and Flov are not bundled.

## Server options

| Option | Default | Purpose |
| --- | --- | --- |
| `--addr` | `127.0.0.1:9830` | Wildcard or loopback HTTP bind address |
| `--https-addr` | Disabled | Optional HTTPS listener in the same server |
| `--tls-cert`, `--tls-key` | — | Certificate chain and private key for the HTTPS listener |
| `--data` | `$XDG_DATA_HOME/connect-bots` or `~/.local/share/connect-bots` | Private persistent data directory |
| `--codex-app-server-url` | Start an owned private app-server | Explicit external dedicated Codex endpoint |
| `--flov-url` | `http://127.0.0.1:17432/v1/audio/transcriptions` | Complete transcription endpoint URL |
| `--assets` | Embedded frontend | Serve a frontend build directory instead |
| `--origins` | Same-origin requests | Additional allowed browser origins, comma separated |
| `--registration` | `true` | Allow new accounts; use `--registration=false` to close registration |
| `--public-url` | Disabled | Explicit public origin advertised in node pairing commands; required to add remote hosts |
| `--allow-insecure-nodes` | `false` | Explicitly allow plaintext HTTP node connections for a trusted LAN |
| `--node-binaries` | Disabled | Directory of node binaries offered by the authenticated download endpoint |
| `--version` | — | Print the binary version and exit |

## Accounts

Open the app and choose **Create account**. Usernames are case-insensitive,
3–32 ASCII characters and can contain letters, digits, `.`, `_` and `-`, starting
with a letter or digit. Passwords are 8–128 bytes. On a fresh installation, the
first account owns the root workspace; every later account receives its own
`users/user_<random>/` workspace and a coordinator. Each account has independent
bots, conversation history, uploads, instructions, product skills and maintenance
settings. Sign in later with the username and password.

Registration is enabled by default. Set `--registration=false` after creating
the desired accounts to allow only existing users to sign in. Browsers receive
opaque HttpOnly, SameSite Strict session cookies that expire after 30 days;
HTTPS also sets Secure. Sessions survive a server restart. Signing out revokes
that browser session and closes its open event stream. Requests stay bound to
the account shown in their tab, so a stale tab cannot change or sign out another
account after a switch.

For an upgrade from token login, the existing workspace stays in place. Choose
**Create account** in the browser with the old valid owner session; registration
links the account to the existing bots, history and folders. A new browser without
that session creates a separate workspace, even if it registers first. If the old
session is unavailable, the host owner can claim the workspace once with
`POST /api/studio/register` using `{username,password,accessKey}`, where `accessKey`
is the legacy value from `<data>/token` or `CONNECT_BOTS_TOKEN`. Supply the same
Origin as the app. Keep this value out of URLs, logs and commits. It is accepted
only for claiming an unowned legacy workspace and cannot be used to sign in.
Fresh installations do not create a login token.

Accounts and session digests are stored in `auth/accounts.json`, not in bot
instructions. Passwords are hashed with bcrypt; raw passwords and session tokens
are not stored. `auth/workspace.json` preserves whether the root needed a legacy
claim. Keep the whole `auth/` directory with backups and do not edit it while the
server is running.

The default install directory contains:

```text
connect-bots/
  auth/
    accounts.json
    workspace.json
  token                         # legacy workspace claim only, if upgrading
  state.json
  events.jsonl
  codex/
    state/
    log/
  run/codex-<random>/server.sock
  user/
    AGENTS.md
    skills/<skill-name>/SKILL.md
  bots/<bot-id>/
    AGENTS.md
    .agents/skills/<skill-name>/SKILL.md
    tmp/
    uploads/
  users/user_<random>/           # one root per additional account
    state.json
    events.jsonl
    user/AGENTS.md
    user/skills/<skill-name>/SKILL.md
    .pi/agent/
      models.json
      settings.json
    bots/<bot-id>/
      AGENTS.md
      .agents/skills/<skill-name>/SKILL.md
      tmp/
      uploads/
```

Use a dedicated data directory outside the Git checkout. Codex authentication
and user configuration remain in its existing home; the owned server's SQLite
state and logs belong to the product directory. The owner retains native Pi
configuration and history; additional accounts use private Pi state inside their
workspace. For a consistent backup, stop the service, copy the whole product data
directory and preserve the owner's native harness history outside it when needed
for full backend-session restoration.

Account separation is enforced by the app and API. All harnesses run as the same
operating-system user and share the host's inference credentials and filesystem
permissions. The shared Codex server retains native host configuration. Use this
setup for trusted users; account folders are not a sandbox for arbitrary shell
commands. Independent OS users or containers are needed for a stronger boundary.

## Codex runtime

By default, Connect Bots launches one `codex app-server` for all accounts' Codex
bots and maintenance. It overrides `sqlite_home` and `log_dir` to
`<data>/codex/state` and `<data>/codex/log`. The inherited Codex home supplies the
existing authentication, user instructions, skills and other user configuration.
No separate login profile is created.

On Unix the child listens through an owner-only socket in a private random
directory under `<data>/run/`; a long socket path uses a private temporary
directory instead. Windows uses a private loopback WebSocket listener. The
socket and child process are cleaned up during shutdown. Server output is in
`<data>/codex/log/app-server.log`, with owner-only permissions.

Startup waits for the owned server's protocol initialization. A missing Codex
executable fails startup. The default readiness timeout is 120 seconds: first
startup can take about a minute while Codex fills its private SQLite index from
existing native history. Later starts reuse that state. If the owned app-server
exits unexpectedly, Connect Bots exits and a supervisor such as the provided
systemd unit can restart it.
Runtime sessions are closed before the child is stopped, so native interruption
and goal cleanup can complete.

An existing dedicated app-server can instead be selected with
`--codex-app-server-url unix:///path/to/private/server.sock` or a concrete
WebSocket URL. Its lifecycle and state directories are then managed externally.
`managed:` is rejected, and Connect Bots does not fall back to the user's common
Codex daemon.

## Local and LAN use

Build with `make -f Makefile.connect-bots build`, then run:

```sh
./bin/connect-bots --addr 0.0.0.0:9830
```

Open `http://<computer-LAN-IP>:9830` from a phone or laptop on the same network.
Account login applies to LAN connections too. Development uses the Vite
frontend on port 5173 with the Go API still on loopback port 9830; see the
[development commands](README.md#development).

### Stable frontend operation

For everyday use, serve a completed frontend build. The normal
`make -f Makefile.connect-bots build` binary embeds it and serves the app on the
API port; editing source files cannot reload an open conversation. The
`--assets /absolute/path/to/studio/dist` option can serve a separately built
frontend from the same Go process.

When keeping an existing LAN URL on port 5173, run the built Studio in preview
mode with the Go API on port 9830:

```sh
pnpm --dir studio build
pnpm --dir studio preview
```

Both the dev server and preview bind to `0.0.0.0:5173` and proxy `/api/studio`
to `127.0.0.1:9830`, including streaming and node WebSocket traffic. Preview
serves `studio/dist` without Vite's hot-reload client or source watching. A
service used for ongoing conversations should run `pnpm --dir studio preview`
instead of `pnpm --dir studio dev`. Rebuild before starting preview; after a
later rebuild, refresh the browser when ready to load the new app.

Host-status polling and background roster refreshes are best effort. A transient
failure retains the last successful host and bot catalog without repeatedly
showing connection-error notifications or resetting the active conversation.
An expired session still requires sign-in, and a failed explicit action still
reports its error. Streaming reconnect resumes the event cursor and does not
send the user's prompt again.

### LAN listeners and microphone access

The HTTP listener must also be reachable over loopback for Pi's local bot tools.
Use `0.0.0.0` (or `[::]` for IPv6) for LAN access, or a loopback address such as
`127.0.0.1`, `[::1]` or `localhost`. Binding to a specific non-loopback LAN IP
is rejected at startup with a working wildcard alternative.

Microphone recording requires HTTPS, except browser-trusted local contexts such
as `localhost`. A phone accessing another machine's HTTP LAN address cannot use
that exception. Audio-file upload remains available.
[Browser requirements](https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getUserMedia).

For direct LAN HTTPS, provide a certificate whose names cover the address you
use, and trust its issuing CA on every accessing device:

```sh
./bin/connect-bots --addr 127.0.0.1:9830 \
  --https-addr 0.0.0.0:5443 \
  --tls-cert /path/to/bots-cert.pem --tls-key /path/to/bots-key.pem
```

Open `https://<certificate-hostname>:5443`. Both listeners belong to the same Go
process; loopback HTTP remains available for internal bot tools. A browser warning
for an untrusted certificate does not provide a reliable microphone setup.

## HTTPS behind a reverse proxy

Alternatively, keep the Go server's HTTP listener on loopback and give an
existing reverse proxy a browser-trusted certificate. The built-in HTTPS
listener is unnecessary in that setup. For example, Caddy can proxy a configured
domain:

```caddyfile
bots.example.com {
    reverse_proxy 127.0.0.1:9830
}
```

Start the server with the browser origin explicitly allowed:

```sh
./bin/connect-bots --addr 127.0.0.1:9830 --origins https://bots.example.com
```

Preserve the request host and forwarded scheme, and allow the SSE connection to
stay open without response buffering. Caddy's reverse proxy handles streaming
responses; see its [streaming documentation](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#streaming).
For private LAN HTTPS, use a certificate trusted by each phone and computer.

## Linux user service

After building, install the binary and the provided user unit:

```sh
install -d -m 700 "$HOME/.local/bin" "$HOME/.config/connect-bots" "$HOME/.config/systemd/user"
install -m 755 bin/connect-bots "$HOME/.local/bin/connect-bots"
install -m 644 examples/connect-bots.service "$HOME/.config/systemd/user/connect-bots.service"
systemctl --user daemon-reload
systemctl --user enable --now connect-bots.service
```

The unit uses the default data directory and binds to loopback. Override
`ExecStart` with `systemctl --user edit connect-bots.service` to change flags.
Use `journalctl --user -u connect-bots.service` for server errors. A user service
needs user lingering if it must stay active after logout.

Keep the provided unit's `KillMode=mixed`: systemd first signals the Go process,
allowing it to close native sessions while its owned Codex server is still
available. Remaining processes in the unit are killed only after the stop timeout.

Optional secrets and harness environment variables belong in
`~/.config/connect-bots/environment`, with permissions `0600`. The service reads
that file; it does not source interactive shell configuration. The owner's Pi
bots can use existing native provider configuration. Additional accounts receive
a private `.pi/agent/models.json` for DeepSeek Flash at the official Anthropic
compatibility endpoint, referencing `DEEPSEEK_API_KEY` from the host environment;
the key value is not copied into that file. Existing account-local Pi files are
preserved. Set the environment variable on the service to enable this default
provider for additional accounts.
Codex authentication and user configuration are inherited by the dedicated child.

For DeepSeek Flash with Pi, the verified provider uses the official Anthropic
compatibility API. In the existing `~/.pi/agent/models.json` DeepSeek provider
and Flash model, set `api` to `anthropic-messages` and `baseUrl` to
`https://api.deepseek.com/anthropic`. Set the model's `compat` to
`{"forceAdaptiveThinking": true}`. Preserve the API-key environment/command
reference, `deepseek-flash` ID, context and output limits. Use a
`thinkingLevelMap` with `low: "low"`, `high: "high"`, `max: "max"` and
unsupported intermediate levels set to `null`. These expose off/low/high/max in
the app. Restart Connect Bots while bots are idle after changing native provider
configuration, so persistent Pi processes reload it. See the
[official compatibility guide](https://api-docs.deepseek.com/guides/anthropic_api/)
and [effort semantics](https://api-docs.deepseek.com/guides/thinking_mode/).

Stop the service before replacing its binary, then restart it. Accepted turns
remain in the journal; a turn interrupted by restart is marked interrupted and
is not automatically replayed.

## macOS and Windows foreground use

On macOS, build with the same Makefile and run `./bin/connect-bots`. Authenticate
the harnesses as the same user and keep the terminal running. A machine that
sleeps also pauses its bots.

To use only the lightweight Mac node with an existing server, follow
[remote node setup](#remote-nodes) instead of building the full web app.

On Windows, build from PowerShell after installing Go, Node.js, pnpm and Codex
CLI, plus Pi if needed:

```powershell
pnpm --dir studio install --frozen-lockfile
pnpm --dir studio run build
go build -o bin/connect-bots.exe ./cmd/connect-bots
.\bin\connect-bots.exe --addr 127.0.0.1:9830 --data "$env:LOCALAPPDATA\ConnectBots"
```

Backend availability depends on the installed harness's app-server/RPC support
for that platform. The app reports an unavailable backend rather than presenting
it as connected. Linux systemd is optional; the product does not require a
container or another application server.

## Remote nodes

A node is a computer paired to one Connect Bots account. It keeps its own bot
folders, history, instructions, skills, uploads and runtime state; the central
server routes the account's browser requests to it. Codex runs as the node's
operating-system user with that computer's native authentication and configuration.
The node starts and owns a separate dedicated Codex app-server. Pairing transfers
a node credential, not the server's Codex login or the account password.

Open **Settings → Hosts → Add host** while signed in. The code is valid for ten
minutes and can be used once. Download the binary for your computer and, in a
terminal on that computer, pair it:

```sh
./connect-bots-node pair --server https://bots.example.com
./connect-bots-node run
```

`pair` asks for the code on stdin. `--code CODE` is also supported, but interactive
entry keeps the code out of shell history. Install and authenticate Codex locally
as the same user before `run`. The node connects outward to the server; it does
not need a public inbound listening port. It reconnects when the connection is
lost. A sleeping or stopped computer appears offline and cannot accept new work.
Removing a host revokes its pairing. To reconnect the same local workspace to
the same account, create a new code and run `pair` with `--replace`.

The roster shows bots from all hosts belonging to the signed-in account. Each
row includes its device name; **Server** and **Mac** checkboxes filter the list,
with both enabled initially. They do not move or stop bots. Click a bot to open
its host's conversation and make that host active for instructions, skills,
files and other workspace actions. This switches the app state without a page
reload. The **Run on** field in **Create bot** defaults to the active host and
can select another online host.

An offline host retains its last known roster in the current account's browser
session and displays an offline indicator. The account's cached catalogs are
cleared on sign-out or an account change; removing a host removes its rows.
Existing bots remain where they were created. In this first version, bot
migration and delegation between hosts are unavailable; coordinator tools
operate within the selected host.

### Node commands and storage

```sh
./connect-bots-node pair --server https://bots.example.com --data /private/node-data
./connect-bots-node run --data /private/node-data \
  --codex-command /absolute/path/to/codex --codex-home /private/codex-home
./connect-bots-node status --data /private/node-data
./connect-bots-node version
```

Use the same `--data` for pairing and running. Its default is
`~/Library/Application Support/Connect Bots/Node` on macOS, and
`$XDG_DATA_HOME/connect-bots/node` or `~/.local/share/connect-bots/node` on Linux.
Private `node.json` holds the pairing credential and server address. The bot
workspace, owned Codex state and logs live beneath the same node data directory.
Keep this directory outside a checkout and preserve it in backups. Do not commit
or share `node.json`.

`status` reports the saved pairing without printing its credential; check
**Settings → Hosts** for live connection status. `pair --replace` deliberately
preserves the workspace. Use a new `--data` directory when pairing to a different
account so previous local conversations and files stay separate.

`--codex-command` defaults to `codex` on PATH. `--codex-home` uses the node user's
native Codex home when omitted; an explicit home must already have the intended
authentication and settings. Remote nodes never inherit the hub owner's native
skills or credentials. `--flov-url` is empty by default on a node; set its full
transcription URL if voice transcription is available there. The node binary does
not bundle Codex, Pi, ffmpeg, model credentials or a web app.

### TLS and explicit LAN mode

Use an HTTPS server URL with a certificate trusted by the node. Plain HTTP beyond
loopback is rejected by default. A TLS reverse proxy must pass the node's connection through
alongside browser HTTP and SSE. Configure `--public-url` with the external HTTPS
origin when the server sits behind a proxy:

```sh
./bin/connect-bots --addr 127.0.0.1:9830 \
  --public-url https://bots.example.com --origins https://bots.example.com
```

`--public-url` must be an origin without a path, credentials, query or fragment.
Without it, the local server still works, but **Add host** is unavailable.

For development on a trusted private LAN, both sides must explicitly permit
plaintext transport:

```sh
./bin/connect-bots --addr 0.0.0.0:9830 \
  --public-url http://192.168.1.15:9830 --allow-insecure-nodes
```

```sh
./connect-bots-node pair --server http://192.168.1.15:9830 --allow-insecure
./connect-bots-node run
```

Pairing persists the explicit insecure setting for subsequent runs. This option
permits plaintext credentials and conversation traffic; use HTTPS outside that
controlled LAN. The node does not disable certificate verification for HTTPS.

### Build and serve macOS binaries

The pure node targets do not require Studio assets or a Node.js/pnpm installation:

```sh
make -f Makefile.connect-bots build-node
make -f Makefile.connect-bots node-darwin-arm64 VERSION=your-release-version
make -f Makefile.connect-bots node-darwin-amd64 VERSION=your-release-version
make -f Makefile.connect-bots release-node-macos VERSION=your-release-version
```

Cross-builds use `CGO_ENABLED=0`. `darwin-arm64` is for Apple Silicon and
`darwin-amd64` for Intel Macs. `release-node-macos` writes raw binaries, archives
containing an executable named `connect-bots-node`, and `SHA256SUMS` into
`dist/connect-bots/`. Both command binaries receive `main.version` through
ldflags. Without an explicit `VERSION`, the Makefile uses the Git description.
These archives are unsigned; packaging does not imply Apple notarization.

To offer the raw binaries in **Settings → Hosts**, point the server at the build
directory:

```sh
./bin/connect-bots --node-binaries /absolute/path/to/dist/connect-bots \
  --public-url https://bots.example.com --origins https://bots.example.com
```

The authenticated download routes are
`GET /api/studio/nodes/binary/darwin/arm64` and
`GET /api/studio/nodes/binary/darwin/amd64`. They read
`connect-bots-node-darwin-arm64` and `connect-bots-node-darwin-amd64` from that
directory. Downloads use the signed-in account session; they do not expose the
rest of that directory. Archives and checksums can separately be attached to a
release. Verify an archive against `SHA256SUMS` before extracting it:

```sh
shasum -a 256 -c SHA256SUMS
tar -xzf connect-bots-node-darwin-arm64.tar.gz
chmod 755 connect-bots-node
```

Keep all four artifacts beside `SHA256SUMS` for that check. Use the Intel archive
instead on an Intel Mac.

### macOS LaunchAgent

Start in a terminal first and confirm the paired host is online. For automatic
startup after login, a user LaunchAgent can run the same paired data directory.
It runs as the logged-in Mac user; it does not keep a sleeping Mac available or
run before that user's login. Authenticate Codex as that user before installing
the agent.

Unlike an interactive shell, launchd does not load `.zshrc`. Use absolute paths
for the node binary, data directory and Codex executable, and supply any required
environment variables explicitly. Replace the paths below with paths on that
Mac; do not put account passwords or API keys into a committed plist.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.connect-bots.node</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/YOUR_USERNAME/.local/bin/connect-bots-node</string>
    <string>run</string>
    <string>--data</string>
    <string>/Users/YOUR_USERNAME/Library/Application Support/Connect Bots/Node</string>
    <string>--codex-command</string>
    <string>/opt/homebrew/bin/codex</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key><false/>
  </dict>
  <key>StandardOutPath</key><string>/Users/YOUR_USERNAME/Library/Logs/connect-bots-node.log</string>
  <key>StandardErrorPath</key><string>/Users/YOUR_USERNAME/Library/Logs/connect-bots-node.log</string>
</dict>
</plist>
```

Save it as `~/Library/LaunchAgents/com.connect-bots.node.plist`, then load it:

```sh
plutil -lint "$HOME/Library/LaunchAgents/com.connect-bots.node.plist"
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.connect-bots.node.plist"
```

On Intel Macs, the installed Codex path may differ. If Codex is a script whose
interpreter comes from PATH, add the installed Node.js directory through the
plist's `EnvironmentVariables` as well. To replace the node binary, stop the
agent while its bots are idle, install the new executable, and load the agent
again. Do not replace or re-pair its existing data to update the executable.

Removing a host causes its running node to stop after revocation. The command
exits successfully in that case, so the `SuccessfulExit` policy above does not
keep restarting a credential that has been revoked. Pair it again deliberately
before restarting the agent.

## Optional integrations

For Telegram, create a dedicated bot token and make it available through a
chosen server environment variable, such as `TELEGRAM_COORDINATOR_TOKEN`. Enter
that variable's name and explicit allowed Telegram user IDs in the owner's bot
settings, then enable the connection. Additional accounts cannot resolve host
environment Telegram tokens. Do not place the token itself in instructions or
skills. A token must have only one polling process; existing cc-connect Telegram
services remain independent. The web app shows the connection status. Account
workspaces load on service startup, so existing connections do not depend on an
open browser.

For voice, install `ffmpeg`, run Flov's OpenAI-compatible transcription endpoint
on the server and point `--flov-url` at its full transcription URL. The browser
uploads audio to Connect Bots, which converts it to WAV and transcribes it on the
server; the phone does not need direct access to Flov. Recordings and uploads are
limited to 25 MiB per file. Transcription fills a draft and does not submit a
message until the user sends it.

Daily maintenance uses the configured junior model and retention period to
inventory each bot's `tmp/`. It keeps reports and never includes instructions,
skills, uploads or durable outputs in that cleanup boundary. Choose a model that
exists in the installed runtime's catalog.

### Publishing Studio while clients remain open

Use a separate live directory instead of serving the build output while Vite
rebuilds it:

```sh
pnpm --dir studio deploy:local /absolute/path/to/studio-live
pnpm --dir studio exec vite preview --outDir /absolute/path/to/studio-live --host 0.0.0.0 --port 5173 --strictPort
```

Publication retains old hashed assets and atomically replaces the index after
copying the new files. Subsequent publications need no preview restart. Retain
old assets while clients from that version may still be open. The deployed LAN
service on mujik uses `/home/billy/.local/share/connect-bots/studio-live`.
