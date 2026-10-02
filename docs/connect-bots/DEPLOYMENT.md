# Deployment

Connect Bots is one Go HTTP server with an embedded React app, local bot
workspaces, username/password accounts and a dedicated Codex app-server child.
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
