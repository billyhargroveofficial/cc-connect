# Validation

## Reproducible product gate

```sh
make -f Makefile.connect-bots check
```

The gate builds TypeScript/Vite, runs the frontend tests, vets every Go package
with `no_web`, and runs race-enabled tests for `bots`, `core`, `agent/codex`,
`agent/pi`, `cmd/connect-bots` and `cmd/connect-bots-node`. It includes core CUJ tests and does not skip
product tests. Building the old web dashboard is not required.

## Account authentication and workspace isolation

The multi-user change replaces normal token login with username/password
registration, server-side sessions and account-scoped workspaces. Its focused
regression suites cover password validation and persistence, session revocation,
fresh and legacy owner claims, cross-account bot/file/event access, internal tool
dispatch and private account runtime configuration. Frontend tests cover account
forms and clearing stale workspace state when sessions or identities change.
Account-binding regressions must reject missing or mismatched request bindings
with `account_changed`, including stale-tab mutations, file/SSE reads and logout.

Run these checks for the account change, followed by the full product gate:

```sh
go test -race ./bots ./cmd/connect-bots
pnpm --dir studio test
pnpm --dir studio build
make -f Makefile.connect-bots check
go test ./core/ -run TestCUJ
```

Browser verification should register two accounts on a disposable host, sign out
and back in, check that bots, instructions, skills, uploads and event streams stay
within their account, and exercise the mobile forms. Check legacy migration with
an existing valid owner cookie separately: it must retain the root workspace,
while an anonymous registration receives a new folder. Tests of browser/API
isolation do not establish an operating-system sandbox; harnesses still share
the host Unix user and Codex/provider credentials.

## Remote host and macOS release checks

The node change adds a separate command and an account-scoped remote workspace.
Run the product gate, then verify node cross-builds without relying on Studio:

```sh
go test -race ./bots ./cmd/connect-bots ./cmd/connect-bots-node
make -f Makefile.connect-bots build-node VERSION=node-check
./bin/connect-bots-node version
make -f Makefile.connect-bots release-node-macos VERSION=node-check
file dist/connect-bots/connect-bots-node-darwin-arm64 \
  dist/connect-bots/connect-bots-node-darwin-amd64
cd dist/connect-bots
shasum -a 256 -c SHA256SUMS
tar -tzf connect-bots-node-darwin-arm64.tar.gz
tar -tzf connect-bots-node-darwin-amd64.tar.gz
```

Cross-compilation verifies target code and packaging, not native Mac execution.
On a disposable Mac workspace, execute the matching architecture's binary,
check `version` and `status`, pair it to a test account, run with the Mac user's
authenticated Codex, and exercise a real answer with a local output file. Stop
and restart the node, verify conversation continuity, then remove the test host
and verify that its old credential cannot reconnect. Record the architecture,
OS and result; do not treat an Intel cross-build as a tested Intel runtime.

Protocol and browser regression coverage must check:

- A pairing code expires, can be used once, and registers only under its issuing
  account. Another account cannot list, select, reconnect or revoke that host.
- Plain HTTP pairing beyond loopback fails by default. The explicit insecure LAN option is
  required on both sides, while HTTPS still verifies certificates and hostnames.
- The node makes an outbound connection, handles reconnects, propagates request
  cancellation and streams events without replaying a prompt.
- Bot creation, history, instructions, skills, uploads, downloads, capabilities,
  compaction and goals resolve on the selected host; offline operations fail
  visibly and local hub bots remain available.
- Stale account or host requests cannot merge bot lists, event sequences,
  attachments or drafts from the previous selection. Every direct file URL
  carries its account and host binding.
- The roster combines bots from the hub and paired devices, with the correct
  device label and independent Server/Mac filters. Equal bot IDs on two hosts
  remain separate rows. Clicking either row opens the correct host without a
  document reload; offline catalogs remain visible until that account signs out
  or the host is removed.
- Revocation closes the connection, cancels pending routed requests and rejects
  future connections. Download paths are restricted to the configured fixed
  binary names and require account authentication.
- Node shutdown closes its own sessions and Codex child, preserves local state,
  and does not stop another Codex daemon on the computer.

The commands and acceptance cases above describe the node release gate. Results
from earlier local-server checks below are not evidence that a node or a Mac
runtime has passed; record the completed node checks separately when run.

## Conversation stability checks

Run the frontend regression suite and production build:

```sh
pnpm --dir studio test
pnpm --dir studio build
```

Verify the built app, either embedded in the Go binary or served with
`pnpm --dir studio preview`. The HTML and modules must not reference Vite's HMR
client. Editing a source file must leave an existing browser document open.
Switch repeatedly between conversations, then leave one idle across several
host polls. Its selected bot, draft, attachment state and scroll position must
remain intact.

Simulate a failed background `/nodes` or inactive-host roster request. The last
successful catalog must remain, with no repeating connection-error toast and no
workspace reset. Recovery must update host status without resending a message.
Confirm separately that a real session expiry requests sign-in and an explicit
failed send, pairing or host removal reports its error.

The Markdown regressions include currency such as `$200`, `$500/month` and
currency followed by real math. Currency must stay ordinary text without
consuming intervening prose; genuine inline/display formulas, escaped dollars,
code and links must keep their intended rendering.

## Upstream suite and external CLIs

```sh
make -f Makefile.connect-bots check-upstream
```

This runs all Go packages with `CI=1`, using upstream's existing skip gates for
real Cursor ACP login, model-list and local-chat-storage probes. Tests of failed
CLI commands and protocol fixtures still run. `GO_TEST_FLAGS` can supply ordinary
Go test options; no named unit test is excluded by default.

The initial unfiltered all-package run on 2026-10-01 failed in external Cursor
CLI probes and `TestClaudeConfigBase_HonorsProjectEnvOverride`. The latter is an
unchanged upstream constructor regression that needs a `claude` executable on
`PATH`; this validation environment did not have one. Adding `~/.local/bin` to
`PATH` therefore did not fix it. No harness was installed and no upstream test
was modified to hide these results.

When that executable is absent, an explicitly limited upstream run is:

```sh
CI=1 go test -tags no_web \
  -skip '^TestClaudeConfigBase_HonorsProjectEnvOverride$' ./...
```

Record that exception with the result. It is not a pass of the unfiltered
upstream suite. To exercise the live Cursor probes separately, use a configured,
authenticated CLI and omit `CI`/`SKIP_REAL_AGENT_CLI` from its test environment.

## Previously recorded local-server gates

These completed product and explicitly limited upstream gates include the
dedicated Codex app-server and account workspaces. They predate remote nodes,
the aggregate roster and the conversation-stability changes above. The
compaction-to-goal regression also passed the full `bots` race suite and vet
after that product gate. Record the new gate separately when completed.

| Check | Result |
| --- | --- |
| `make -f Makefile.connect-bots check` completed run | PASS: 163 frontend tests, TypeScript/Vite build, all-package Go vet and product/core/adapter/command tests with the race detector |
| Explicitly limited all-package Go suite | PASS with `CI=1`, `no_web` and the single Claude constructor exception shown above |
| New product workflow checked by `actionlint` | PASS |
| Latest frontend activity/composer tests and build | PASS: 163 tests and production build, including multi-account identity races, exact compaction correlation, request-lifetime spinner and preservation of drafts/files added during an in-flight send |

## Dedicated Codex runtime verification

| Check after the runtime change | Result |
| --- | --- |
| Owned child PID and private endpoint differ from the user's common daemon | PASS on 2026-10-02: fresh isolated startup in 24.8 s; a distinct child PID used a private Unix socket, SQLite state and logs beneath disposable product data; the socket directory had owner-only permissions |
| Embedded assets, HTTPS owner login and authenticated SSE shutdown with owned child cleanup | PASS: index, JavaScript and favicon returned 200; the client verified the certificate and hostname using private trust; HTTP cookies were HttpOnly/Strict, HTTPS also Secure; anonymous access returned 401, owner access 200 and foreign-origin editing 403; SIGTERM with HTTP and HTTPS SSE open exited 0 in 31 ms, closed both streams and removed the owned child, socket and runtime directory |
| Existing common Codex daemon remains unchanged | PASS: common daemon PIDs, start times, command hashes and listening sockets matched before and after; existing cc-connect and MathAcademy service identities were unchanged; no native thread or inference was created; all isolated processes, data, owner token, binary, certificate and key were removed |
| Real Codex answer and thread persistence on the dedicated server | PASS: a real answer recalled earlier conversation context; the native thread and paused goal survived an owned-process restart |
| Native goal restart and completion on the dedicated server | PASS: paused objective/budget survived restart, the bot recalled the saved marker, a real goal reached `complete`, and an earlier bounded goal reached the native budget limit |
| Native compaction and Stop on the dedicated server | PASS: native compaction completed without a chat turn, retained estimated provider context; Stop interrupted its exact native turn and cleared the busy state |
| Junior temporary-file inventory on the dedicated server | PASS: real junior-model inventory removed the expired fixture, retained the fresh file and created no conversation turn |
| Updated product and explicitly limited upstream Go gates | PASS: product gate and explicitly limited all-package suite completed with dedicated runtime; final compact-to-goal regression passed full `bots` race suite and vet |

## Earlier transport evidence

This transport smoke was performed before the dedicated Codex child was added;
it does not establish cleanup or isolation of the new child process.

| Check | Observed result |
| --- | --- |
| Product Makefile parsing | `make -n` completed successfully |
| Product and upstream gates | Completed results are listed above |
| Documentation links | Local targets resolved |
| Targeted upstream Cursor probes with `CI=1` | Skipped through their existing upstream gates |
| Targeted Claude constructor regression after adding the user binary path | Failed: local `claude` executable absent |
| Production embedded frontend over HTTPS | Index and referenced JavaScript returned 200 |
| Owner login over HTTP and HTTPS | HttpOnly/Strict cookies; `Secure` present only over HTTPS |
| HTTPS API and origins | Anonymous bots request 401; authenticated request 200; same-origin edit 200; foreign-origin edit 403 |
| SIGTERM with HTTP and HTTPS SSE open | Both streams active; process exited 0 in 3 ms |

The production transport smoke used a freshly built binary, a disposable data
directory, loopback ports 19831/19832 and a one-day self-signed certificate with
`localhost` SAN. The test client verified the certificate and hostname using a
private trust context; system trust was not changed. No inference was requested.
The test process, data, token, binary, certificate and key were removed afterward.
This verifies the server transport; actual phone microphone behavior still needs
browser testing with a certificate trusted on that device.

## Live workflow evidence

These earlier Codex and maintenance checks used the previous shared-server
runtime. They remain historical evidence; dedicated-server results are recorded
separately above. The final Pi provider check is recorded separately below.

| Workflow | Observed result |
| --- | --- |
| Coordinator delegation | The real coordinator delegated to another bot and returned its correct answer |
| Codex capabilities | Real Codex runs exercised ultra, subagents and native web search of official sources |
| Native interruption | Stop terminated the background process; its delayed artifact was not created |
| Backend and model changes | Pi-to-Codex handoff preserved visible conversation context; a model change within one backend retained its native thread |
| File round trip | Text and PNG inputs reached a real bot; `bots_publish_files` published text, PNG and ZIP downloads with the expected content after temporary copies |
| Browser layouts and compaction | Desktop, mobile and light layouts checked; native compaction displayed a spinner, disabled model/send controls and retained its activity after completion |
| Multiple clients | Two authenticated SSE clients received the same global mutation; the owner cookie continued to authenticate |
| Voice drafts | Real Flov transcribed WAV and WebM into drafts without submitting a bot message |
| Junior-model inventory | Only the expired temporary file was cleaned; external paths and symlink targets were untouched |
| Native goals after restart | Paused goal state survived restart, a pending conversation completed while it remained paused, and explicit activation completed the native goal with usage accounting |
| Manual compaction | Compaction became active and completed on the same thread without extra chat turns; a later answer recalled prior uploaded context without tools |
| Context usage after compaction | Provider-reported `5864/258400` usage, labelled an estimate, remained after completion without another chat turn |
| Stopping native compaction | Stop returned 200, interrupted compaction's own native turn, canceled its worker and cleared busy state without hanging |

These entries summarize real runtime checks without publishing thread IDs,
conversation contents, tokens or owner configuration. Completed gates and the dedicated-server checks are recorded above.
Actual phone microphone recording and live Telegram
delivery remain separate checks where those integrations are configured.

## Real Pi and DeepSeek verification

PASS: a real Connect Bots Pi conversation on `deepseek/deepseek-flash` with
maximum effort returned the expected completed answer. The normal configured
output ceiling of `384000` was retained. The journal contained the actual Pi
stream, terminal `agent_settled`, provider output-token usage and a completed
product turn. A further real Pi run delegated to a Codex bot, awaited its correct
result and published a file whose authenticated download matched that result.
A real Pi-to-Codex-to-Pi switch retained the visible result in both directions
and preserved the original Pi native session. Independent isolated Pi checks
also completed with thinking off
and maximum effort; the maximum-effort stream included exposed thinking.

The owner's existing Pi DeepSeek provider and Flash model now use
`anthropic-messages` at `https://api.deepseek.com/anthropic`, with
`compat.forceAdaptiveThinking: true`. Credential commands, model IDs, context and
output limits, and the low/high/max effort map were retained. No provider secret
or native owner configuration is committed. Both provider-level and model-level
endpoints must agree when a model overrides its provider's URL.

Earlier OpenAI-compatible requests returned HTTP 200 with waiting keep-alives
without a completed answer, including a direct probe outside Pi. The verified
Anthropic-compatible path resolved that integration limitation. This does not
establish a global outage or a permanent fault in the other API.
[DeepSeek Anthropic API](https://api-docs.deepseek.com/guides/anthropic_api/),
[thinking and effort](https://api-docs.deepseek.com/guides/thinking_mode/),
[model limits](https://api-docs.deepseek.com/api/list-models/).

## Fork baseline

The product branch starts at `aa3c91ba6e843df82e97d315550571725a63b8e2`, also
the fork's remote `telegram-rich` head at inspection. The remote `main` was
`580df5ea98838282fa9e3580c4cf2f35a0cb8e6c`, 40 commits behind that baseline.
A product PR against `telegram-rich` isolates the new product changes; targeting
that older `main` also includes 70 pre-existing changed files. Existing
Telegram-rich and managed-app-server work is part of the inherited baseline;
Connect Bots uses its dedicated runtime instead of that managed default.

### Frontend performance audit (2026-10-02)

See [frontend-performance.md](frontend-performance.md) for the complete surface
inventory, before/after render evidence, regression coverage and limits. The
repeatable synthetic browser fixture is `pnpm --dir studio bench:render` and
never connects to actual bot workspaces. The LAN preview now serves a separately
published directory so normal source builds cannot invalidate open clients.
