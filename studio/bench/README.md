# Render benchmark

Run `pnpm bench:render`, then open `http://127.0.0.1:5187` in the existing browser.
It renders 100 completed turns (3,500+ events), Markdown/code/math and one live
commentary item. `?full` renders the entire workspace with fake accounts, API and
SSE. No requests or messages reach the real bot service.

The server injects render counters only into this fixture. In the console:

```js
// ChatRoom fixture: synchronous token-update costs, including React commit.
const times = Array.from({length: 20}, () => window.perfStep()).sort((a, b) => a - b)
console.log({median: times[10], p95: times[19]})

// Full workspace: a burst should update only the active turn and Markdown.
globalThis.__counts = {}
window.emitBurst(100)
setTimeout(() => console.log(globalThis.__counts), 200)

// Events for the other bot should not render the selected chat or roster.
globalThis.__counts = {}
window.emitBurst(100, 'other')
setTimeout(() => console.log(globalThis.__counts), 200)
```

Append `?motion` (or `?full&motion`) to exercise the real transitions. The
default disables motion so render timings do not include animation frames.

`window.samples` contains actual React Profiler commits/durations. Also verify
idle node polling, typing, switching bots twice, scroll position during streaming,
expanding details, and a narrow viewport. Clear counters after initial mounting.
These are development-mode measurements; compare the same browser, machine,
viewport and fixture. They are not production latency or Mac/Safari guarantees.

The fixture is outside `src` and is not included in the production bundle.
