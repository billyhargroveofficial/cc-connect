import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import * as hostCatalog from '../lib/hostCatalog.ts';
import * as capabilityCatalog from '../lib/capabilityCatalog.ts';
import { mergeEvents as realMergeEvents } from '../lib/events.ts';
import { botHistoryEntry } from '../lib/botHistory.ts';

const source = ts.transpileModule(readFileSync(new URL('./useWorkspace.ts', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText;
const journalSource = ts.transpileModule(readFileSync(new URL('../lib/eventJournal.ts', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText;
const journalModule = {};
runInNewContext(journalSource, { exports: journalModule, require: name => ({ './events': { mergeEvents: realMergeEvents }, './botHistory': { botHistoryEntry } })[name] });
const session = id => ({ authenticated: true, user: { id, username: id.toLowerCase() }, registrationAllowed: true });
const bot = (id, name) => ({ id, name, status: 'idle', backend: 'codex', model: 'model', createdAt: '2026-10-02T10:00:00Z' });
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
class ApiError extends Error {
  constructor(message, status, code) { super(message); this.status = status; this.code = code; }
}

function fixture(initialSession = session('account-a'), storage = new Map()) {
  const slots = [];
  const sources = [];
  const probes = [];
  const requests = { bots: [], history: [], capabilities: [], nodes: [], login: [], register: [], logout: [] };
  const timers = new Map();
  let timerId = 0;
  let apiAccount = null, apiNode = 'local';
  const accountListeners = new Set();
  let cookieSession = initialSession;
  const sessionQueue = [];
  const logoutQueue = [];
  let index = 0, dirty = false, effects = [], value;
  const equal = (left, right) => !!left && left.length === right.length && right.every((v, i) => Object.is(v, left[i]));
  const react = {
    useState(initial) {
      const i = index++;
      if (!(i in slots)) {
        const slot = { value: typeof initial === 'function' ? initial() : initial };
        slot.set = next => {
          const changed = typeof next === 'function' ? next(slot.value) : next;
          if (!Object.is(changed, slot.value)) { slot.value = changed; dirty = true; }
        };
        slots[i] = slot;
      }
      return [slots[i].value, slots[i].set];
    },
    useReducer(reducer, initial) {
      const [state, setState] = react.useState(initial);
      const ref = react.useRef(null);
      ref.current ??= action => setState(previous => reducer(previous, action));
      return [state, ref.current];
    },
    useRef(initial) { const i = index++; return slots[i] ??= { current: initial }; },
    useMemo(factory, deps) {
      const i = index++;
      if (!equal(slots[i]?.deps, deps)) slots[i] = { deps, value: factory() };
      return slots[i].value;
    },
    useCallback(callback, deps) {
      const i = index++;
      if (!equal(slots[i]?.deps, deps)) slots[i] = { deps, callback };
      return slots[i].callback;
    },
    useEffect(effect, deps) {
      const i = index++;
      if (!equal(slots[i]?.deps, deps)) {
        const old = slots[i];
        const slot = { deps, cleanup: undefined };
        slots[i] = slot;
        effects.push(() => { old?.cleanup?.(); slot.cleanup = effect(); });
      }
    },
  };
  function pending(kind, args) {
    const result = deferred();
    requests[kind].push({ ...result, args, account: apiAccount, node: apiNode });
    return result.promise;
  }
  const api = {
    session: async () => sessionQueue.length ? await sessionQueue.shift() : cookieSession,
    nodes: (...args) => pending('nodes', args),
    bots: (...args) => pending('bots', args),
    events: id => pending('history', [id]),
    capabilities: (...args) => pending('capabilities', args),
    login: async credentials => { requests.login.push(credentials); cookieSession = session(credentials.username); return cookieSession; },
    register: async credentials => { requests.register.push(credentials); cookieSession = session(credentials.username); return cookieSession; },
    logout: async expectedAccount => { requests.logout.push(expectedAccount); if (logoutQueue.length) await logoutQueue.shift(); cookieSession = { authenticated: false, registrationAllowed: true }; },
  };
  class FakeEventSource {
    constructor(url) { this.url = url; this.listeners = new Map(); this.closed = false; sources.push(this); }
    addEventListener(name, listener) { this.listeners.set(name, listener); }
    close() { this.closed = true; }
    open() { this.onopen?.(); }
    fail() { this.onerror?.(); }
    emit(event) { this.listeners.get('event')?.({ data: JSON.stringify(event) }); }
  }
  const modules = {
    react,
    '../lib/api': {
      api, ApiError, errorMessage: error => error.message,
      setApiAccount(accountId) { apiAccount = accountId; },
      setApiNode(nodeId) { apiNode = nodeId; },
      isAccountChanged: error => error instanceof ApiError && error.code === 'account_changed',
      onApiAccountChanged(listener) { accountListeners.add(listener); return () => accountListeners.delete(listener); },
    },
    '../lib/events': {
      mergeBots: (previous, incoming) => [...new Map([...previous, ...incoming].map(bot => [bot.id, bot])).values()],
      mergeEvents: (previous, incoming) => [...new Map([...previous, ...incoming].map(event => [event.seq, event])).values()],
      eventBot: event => event.type === 'bot' ? event.data.bot : null,
    },
    '../lib/sessionProbe': {
      createSessionProbe(options) {
        const probe = { options, disposed: false, reconnect() {}, connected() {}, dispose() { this.disposed = true; } };
        probes.push(probe);
        return probe;
      },
    },
    '../lib/hostCatalog': hostCatalog,
    '../lib/capabilityCatalog': capabilityCatalog,
    '../lib/eventJournal': journalModule,
  };
  const exports = {};
  runInNewContext(source, {
    exports, EventSource: FakeEventSource, AbortController, require: name => modules[name],
    localStorage: { getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) },
    setTimeout: (callback, delay) => { const id = ++timerId; callback.delay = delay; timers.set(id, callback); return id; },
    clearTimeout: id => timers.delete(id),
  }, { filename: 'useWorkspace.ts' });
  function render() {
    do {
      dirty = false; index = 0; effects = [];
      value = exports.useWorkspace();
      for (const effect of effects) effect();
    } while (dirty);
    return value;
  }
  async function flush() {
    for (let i = 0; i < 3; i++) { await new Promise(resolve => setImmediate(resolve)); render(); }
    return value;
  }
  render();
  return {
    get value() { return render(); }, sources, probes, requests, sessionQueue, logoutQueue, render, flush, storage, timers,
    get apiBinding() { return { account: apiAccount, node: apiNode }; },
    botRequest(nodeId, index = -1) { return requests.bots.filter(request => request.args[0] === nodeId).at(index); },
    setCookie(next) { cookieSession = next; },
    changed(accountId) { for (const listener of accountListeners) listener(accountId); },
    dispose() { for (const slot of slots) slot?.cleanup?.(); },
  };
}

const event = (seq, botId = 'shared') => ({ seq, botId, type: 'message', time: '2026-10-02T10:00:00Z', data: { text: 'Private account history' } });
const node = (id, name = id) => ({ id, name, status: 'online', online: true, local: id === 'local' });
const modelCatalog = (id = 'model') => ({ models: [{ id, name: id, backend: 'codex', efforts: ['max'] }],
  backends: { codex: { available: true }, pi: { available: false, reason: 'Not installed' } }, voice: false });
async function openCatalog(view, nodeId = 'local') {
  view.botRequest(nodeId).resolve({ bots: [bot('shared', 'Bot')] });
  await view.flush();
  view.value.selectCatalogBot('shared');
  await view.flush();
  return view.requests.capabilities.at(-1);
}
function tickCatalogRetry(view) {
  const [id, tick] = [...view.timers].find(([, callback]) => callback.delay < 10000);
  view.timers.delete(id);
  tick();
}

test('model catalog survives a timed-out refresh with bounded retries and no chat toast', async () => {
  const view = fixture();
  await view.flush();
  const first = await openCatalog(view);
  const catalog = modelCatalog();
  first.resolve(catalog);
  await view.flush();
  assert.equal(view.value.capabilities, catalog);
  view.value.refreshCapabilities();
  await view.flush();
  assert.equal(view.value.capabilities, catalog, 'refreshing never clears the advertised efforts');
  for (let attempt = 0; attempt < 3; attempt++) {
    view.requests.capabilities.at(-1).reject(new ApiError('context deadline exceeded', 504));
    await view.flush();
    assert.equal(view.value.capabilities, catalog);
    assert.equal(view.value.error, '', 'discovery errors stay inside the picker');
    if (attempt < 2) { assert.equal(view.value.capabilitiesLoading, true); tickCatalogRetry(view); }
  }
  assert.equal(view.value.capabilitiesLoading, false);
  assert.match(view.value.capabilitiesError, /Try again/);
  assert.equal(view.requests.capabilities.length, 4, 'a refresh performs at most three attempts');
  view.value.refreshCapabilities();
  await view.flush();
  view.requests.capabilities.at(-1).resolve(modelCatalog('recovered'));
  await view.flush();
  assert.equal(view.value.capabilities.models[0].id, 'recovered');
  assert.equal(view.value.capabilitiesError, '');
  view.dispose();
});

test('initial model discovery retries and a degraded response retains only known unavailable model metadata', async () => {
  const view = fixture();
  await view.flush();
  (await openCatalog(view)).reject(new ApiError('context deadline exceeded', 504));
  await view.flush();
  assert.equal(view.value.capabilities, null, 'an unknown host never receives invented fallback models');
  assert.equal(view.value.capabilitiesLoading, true);
  assert.equal(view.value.error, '');
  tickCatalogRetry(view);
  view.requests.capabilities.at(-1).resolve(modelCatalog());
  await view.flush();
  view.value.refreshCapabilities();
  await view.flush();
  view.requests.capabilities.at(-1).resolve({ models: [], voice: false,
    backends: { codex: { available: false, reason: 'context deadline exceeded' }, pi: { available: false, reason: 'Not installed' } } });
  await view.flush();
  assert.equal(view.value.capabilities.models[0].id, 'model');
  assert.equal(view.value.capabilities.backends.codex.available, false, 'stale metadata cannot enable unavailable inference');
  assert.equal(view.value.capabilitiesLoading, true);
  view.dispose();
  assert.equal(view.timers.size, 0, 'automatic discovery retries are cancelled on unmount');
});

test('an instructions-driven thread replacement preserves the successful model catalog through a timeout', async () => {
  const view = fixture();
  await view.flush();
  const first = await openCatalog(view);
  const catalog = modelCatalog();
  first.resolve(catalog);
  await view.flush();
  view.value.updateBot({ ...view.value.bots[0], threads: { codex: 'new-thread-after-instructions-change' } });
  await view.flush();
  assert.equal(view.value.capabilities, catalog, 'model settings belong to the configured model rather than one session');
  assert.equal(view.requests.capabilities.length, 1, 'a thread rollover does not rediscover unchanged model settings');
  view.value.refreshCapabilities();
  await view.flush();
  view.requests.capabilities.at(-1).reject(new ApiError('context deadline exceeded', 504));
  await view.flush();
  assert.equal(view.value.capabilities, catalog, 'a subsequent timeout retains the pre-rollover catalog');
  assert.equal(view.value.error, '');
  view.dispose();
});

test('cached model catalogs stay isolated by account and host and reject late retry results', async () => {
  const view = fixture();
  await view.flush();
  view.requests.nodes[0].resolve({ nodes: [node('local'), node('mac-a')] });
  (await openCatalog(view)).resolve(modelCatalog('private-local'));
  await view.flush();
  view.value.selectNode('mac-a');
  await view.flush();
  const remote = await openCatalog(view, 'mac-a');
  assert.equal(view.value.capabilities, null, 'another host cannot inherit the local catalog');
  assert.equal(remote.args[2], 'mac-a');
  assert.equal(remote.args[3], 'account-a');
  remote.resolve(modelCatalog('private-mac'));
  await view.flush();
  view.value.selectNode('local');
  await view.flush();
  const refresh = await openCatalog(view);
  assert.equal(view.value.capabilities.models[0].id, 'private-local', 'returning to a host restores only its exact catalog scope');
  refresh.reject(new ApiError('context deadline exceeded', 504));
  await view.flush();
  tickCatalogRetry(view);
  const late = view.requests.capabilities.at(-1);
  const old = view.value;
  await old.logout();
  await view.value.login({ username: 'account-b', password: 'password' });
  await view.flush();
  const fresh = await openCatalog(view);
  assert.equal(view.value.capabilities, null, 'a new account has no cached model metadata');
  assert.equal(late.args[1].aborted, true);
  late.resolve(modelCatalog('leaked-a'));
  old.refreshCapabilities();
  await view.flush();
  assert.equal(view.value.capabilities, null);
  fresh.resolve(modelCatalog('account-b'));
  await view.flush();
  assert.equal(view.value.capabilities.models[0].id, 'account-b');
  view.dispose();
});

test('switching nodes resets duplicate bot IDs and event cursors and rejects every late workspace result', async () => {
  const view = fixture();
  await view.flush();
  view.requests.nodes[0].resolve({ nodes: [node('local', 'This server'), node('mac-b', 'MacBook')] });
  view.requests.bots[0].resolve({ bots: [bot('shared', 'Local bot')] });
  await view.flush();
  view.sources[0].emit(event(99));
  view.value.selectCatalogBot('shared');
  await view.flush();
  const before = view.value;
  const history = before.loadHistory('shared');
  const oldCatalog = view.requests.capabilities[0];
  before.selectNode('mac-b');
  assert.equal(view.value.bots.length, 0);
  assert.equal(Object.keys(view.value.events).length, 0);
  assert.equal(view.value.capabilities, null);
  assert.equal(view.value.activeNode.id, 'mac-b');
  assert.equal(view.value.accountVersion, before.accountVersion, 'host selection keeps the account identity');
  assert.ok(view.value.workspaceVersion > before.workspaceVersion);
  assert.equal(view.sources[0].closed, true);
  await view.flush();
  assert.equal(view.sources[1].url, '/api/studio/events?after=0&expectedAccount=account-a&node=mac-b');
  assert.deepEqual(view.apiBinding, { account: 'account-a', node: 'mac-b' });
  view.botRequest('mac-b').resolve({ bots: [bot('shared', 'Mac bot')] });
  oldCatalog.resolve({ models: [{ id: 'local-secret-model' }] });
  view.requests.history[0].resolve({ events: [event(100)] });
  await history;
  view.sources[0].emit({ ...event(101), type: 'bot', data: { bot: bot('leaked', 'Local private bot') } });
  before.updateBot(bot('leaked-callback', 'Local private callback'));
  before.archiveBot('shared');
  before.setError('Local private error');
  await view.flush();
  assert.deepEqual(Array.from(view.value.bots, item => item.name), ['Mac bot']);
  assert.equal(Object.keys(view.value.events).length, 0);
  assert.equal(view.value.capabilities, null);
  assert.equal(view.value.error, '');
  view.value.selectCatalogBot('shared');
  await view.flush();
  assert.equal(view.requests.capabilities.at(-1).args[2], 'mac-b', 'the model catalog is explicitly scoped to the selected host');
  assert.equal(view.storage.get('connect-bots:account:account-a:node'), 'mac-b');
  view.dispose();
});

test('the roster aggregates duplicate IDs from every owned online host with explicit account and node requests', async () => {
  const view = fixture();
  await view.flush();
  view.requests.nodes[0].resolve({ nodes: [node('local', 'Server'), { ...node('mac-a', 'MacBook'), os: 'darwin' }] });
  view.botRequest('local').resolve({ bots: [bot('shared', 'Server bot')] });
  await view.flush();
  const remote = view.botRequest('mac-a');
  assert.equal(remote.args[0], 'mac-a');
  assert.equal(remote.args[2], 'account-a');
  assert.equal(remote.args[1].aborted, false);
  remote.resolve({ bots: [bot('shared', 'Mac bot')] });
  await view.flush();
  assert.deepEqual(Array.from(view.value.catalogBots, entry => [entry.node.id, entry.bot.name]), [
    ['local', 'Server bot'], ['mac-a', 'Mac bot'],
  ]);
  assert.notEqual(view.value.catalogBots[0].key, view.value.catalogBots[1].key);
  assert.deepEqual(Array.from(view.value.bots, item => item.name), ['Server bot'], 'chat data remains scoped to the active node');
  assert.equal(view.sources.length, 1, 'the aggregate roster does not open a stream for each host');
  assert.deepEqual(view.apiBinding, { account: 'account-a', node: 'local' });
  view.dispose();
});

test('newer catalog refreshes beat late snapshots and offline rows remain in session until their host is removed', async () => {
  const view = fixture();
  await view.flush();
  view.requests.nodes[0].resolve({ nodes: [node('local'), node('mac-a', 'MacBook')] });
  view.botRequest('local').resolve({ bots: [bot('shared', 'Server bot')] });
  await view.flush();
  const stale = view.botRequest('mac-a');
  const refresh = view.value.refreshNodes();
  view.requests.nodes[1].resolve({ nodes: [node('local'), node('mac-a', 'Renamed MacBook')] });
  await refresh;
  await view.flush();
  assert.equal(stale.args[1].aborted, true);
  view.botRequest('mac-a').resolve({ bots: [bot('shared', 'Current Mac bot')] });
  stale.resolve({ bots: [bot('shared', 'Stale Mac bot')] });
  await view.flush();
  assert.equal(view.value.catalogBots.find(entry => entry.node.id === 'mac-a').bot.name, 'Current Mac bot');
  const offline = view.value.refreshNodes();
  view.requests.nodes[2].resolve({ nodes: [node('local'), { ...node('mac-a', 'Renamed MacBook'), online: false, status: 'offline' }] });
  await offline;
  await view.flush();
  assert.equal(view.value.catalogBots.find(entry => entry.node.id === 'mac-a').node.online, false);
  view.value.setHostFilter('server', false);
  view.value.selectNode('mac-a');
  await view.flush();
  assert.equal(view.value.hostFilters.server, false, 'device filters survive switching the active workspace');
  assert.deepEqual(Array.from(view.value.bots, item => item.name), ['Current Mac bot'], 'offline selection can show its last known bot');
  assert.equal(view.value.activeNode.online, false);
  const remove = view.value.refreshNodes();
  view.requests.nodes[3].resolve({ nodes: [node('local')] });
  await remove;
  await view.flush();
  assert.equal(view.value.activeNodeId, 'local');
  assert.equal(view.value.catalogBots.some(entry => entry.node.id === 'mac-a'), false);
  view.dispose();
});

test('aggregate catalogs and filters never survive an account change or accept that account late responses', async () => {
  const view = fixture();
  await view.flush();
  view.requests.nodes[0].resolve({ nodes: [node('local'), node('private-mac-a', 'MacBook')] });
  view.botRequest('local').resolve({ bots: [bot('shared', 'Private server A')] });
  await view.flush();
  const stale = view.botRequest('private-mac-a');
  view.value.setHostFilter('server', false);
  const old = view.value;
  await old.logout();
  await view.value.login({ username: 'account-b', password: 'password' });
  await view.flush();
  assert.equal(view.value.catalogBots.length, 0);
  assert.equal(view.value.hostFilters.server, true);
  view.requests.nodes[1].resolve({ nodes: [node('local'), node('mac-b', 'MacBook B')] });
  view.botRequest('local').resolve({ bots: [bot('shared', 'Server B')] });
  stale.resolve({ bots: [bot('shared', 'Private late Mac A')] });
  old.setHostFilter('mac', false);
  await view.flush();
  view.botRequest('mac-b').resolve({ bots: [bot('shared', 'Mac B')] });
  await view.flush();
  assert.deepEqual(Array.from(view.value.catalogBots, entry => entry.bot.name), ['Server B', 'Mac B']);
  assert.equal(view.value.hostFilters.mac, true);
  assert.equal(stale.args[1].aborted, true);
  view.dispose();
});

test('host lists poll within their account, ignore stale refreshes, and reset a removed selected node to local', async () => {
  const view = fixture();
  await view.flush();
  view.requests.nodes[0].resolve({ nodes: [node('local'), node('mac-a')] });
  await view.flush();
  view.value.selectNode('mac-a');
  await view.flush();
  const first = view.value.refreshNodes();
  const second = view.value.refreshNodes();
  view.requests.nodes[2].resolve({ nodes: [node('local'), node('mac-a', 'New name')] });
  await second;
  view.requests.nodes[1].resolve({ nodes: [node('local')] });
  await first;
  await view.flush();
  assert.equal(view.value.activeNode.id, 'mac-a', 'an older host snapshot cannot remove the current host');
  assert.equal(view.value.activeNode.name, 'New name');
  assert.equal(view.timers.size, 1);
  const [timerId, tick] = [...view.timers][0];
  view.timers.delete(timerId);
  tick();
  assert.equal(view.requests.nodes.length, 4, 'one delayed poll refreshes host connectivity');
  view.requests.nodes[3].resolve({ nodes: [node('local')] });
  await view.flush();
  assert.equal(view.value.activeNode.id, 'local');
  assert.equal(view.apiBinding.node, 'local');
  assert.equal(view.storage.get('connect-bots:account:account-a:node'), 'local');
  assert.equal(view.sources[1].closed, true);
  view.dispose();
  assert.equal(view.timers.size, 0, 'account-owned polling is cancelled on disposal');
});

test('background host polling keeps conversations and workspace identity through unsupported hubs and outages', async () => {
  const view = fixture();
  await view.flush();
  const before = view.value;
  view.requests.nodes[0].reject(new ApiError('Could not connect to the server.', 404));
  view.botRequest('local').resolve({ bots: [bot('shared', 'Current conversation')] });
  view.sources[0].emit(event(51));
  await view.flush();
  for (const cause of [new TypeError('Failed to fetch'), new ApiError('Could not connect to the server.', 502)]) {
    assert.equal(view.value.error, '', 'an optional background endpoint cannot raise a chat-wide toast');
    assert.equal(view.value.workspaceVersion, before.workspaceVersion);
    assert.equal(view.value.accountVersion, before.accountVersion);
    assert.equal(view.value.loadHistory, before.loadHistory);
    assert.equal(view.sources.length, 1, 'polling does not replace the workspace stream');
    assert.equal(view.sources[0].closed, false);
    assert.equal(view.value.events.shared[0].seq, 51);
    const [timerId, tick] = [...view.timers][0];
    view.timers.delete(timerId);
    tick();
    view.requests.nodes.at(-1).reject(cause);
    await view.flush();
  }
  assert.equal(view.value.error, '');
  assert.equal(view.value.bots[0].name, 'Current conversation');
  view.dispose();
});

test('silent refresh retains known hosts while explicit refresh still reports the failure', async () => {
  const view = fixture();
  await view.flush();
  view.requests.nodes[0].resolve({ nodes: [node('local'), node('mac-a', 'MacBook')] });
  await view.flush();
  const before = view.value;
  const background = before.refreshNodes({ background: true });
  view.requests.nodes.at(-1).reject(new TypeError('Temporary network outage'));
  const retained = await background;
  await view.flush();
  assert.deepEqual(Array.from(retained, host => host.id), ['local', 'mac-a']);
  assert.equal(view.value.error, '');
  assert.equal(view.value.workspaceVersion, before.workspaceVersion);
  const manual = view.value.refreshNodes();
  view.requests.nodes.at(-1).reject(new TypeError('Manual refresh failed'));
  await assert.rejects(manual, /Manual refresh failed/);
  assert.equal(view.value.error, 'Manual refresh failed');
  view.dispose();
});

test('background authentication expiry still clears account-owned conversation state', async () => {
  const view = fixture();
  await view.flush();
  view.sources[0].emit(event(52));
  view.requests.nodes[0].reject(new ApiError('unauthorized', 401));
  await view.flush();
  assert.equal(view.value.phase, 'login');
  assert.equal(view.value.user, null);
  assert.equal(Object.keys(view.value.events).length, 0);
  assert.equal(view.sources[0].closed, true);
  assert.match(view.value.error, /expired/);
  view.dispose();
});

test('background device catalog outages retain the roster without a global error', async () => {
  const view = fixture();
  await view.flush();
  view.requests.nodes[0].resolve({ nodes: [node('local'), node('mac-a', 'MacBook')] });
  view.botRequest('local').resolve({ bots: [bot('local-bot', 'Server bot')] });
  await view.flush();
  view.botRequest('mac-a').resolve({ bots: [bot('mac-bot', 'Retained Mac bot')] });
  await view.flush();
  const [timerId, tick] = [...view.timers][0];
  view.timers.delete(timerId);
  tick();
  view.requests.nodes.at(-1).resolve({ nodes: [node('local'), node('mac-a', 'MacBook')] });
  await view.flush();
  view.botRequest('mac-a').reject(new ApiError('The server is temporarily unavailable.', 503));
  await view.flush();
  assert.equal(view.value.error, '');
  assert.equal(view.value.catalogBots.find(entry => entry.node.id === 'mac-a').bot.name, 'Retained Mac bot');
  assert.equal(view.sources.length, 1);
  view.dispose();
});

test('a new account cannot inherit another account host list, selection or retained node callback', async () => {
  const view = fixture();
  await view.flush();
  const old = view.value;
  await old.logout();
  await view.value.login({ username: 'account-b', password: 'password' });
  await view.flush();
  view.requests.nodes[1].resolve({ nodes: [node('local'), node('mac-b')] });
  view.requests.nodes[0].resolve({ nodes: [node('local'), node('private-a-node')] });
  await view.flush();
  assert.deepEqual(Array.from(view.value.nodes, item => item.id), ['local', 'mac-b']);
  old.selectNode('private-a-node');
  await old.refreshNodes();
  view.value.selectNode('private-a-node');
  assert.equal(view.value.activeNode.id, 'local');
  assert.equal(view.requests.nodes.length, 2, 'an exiting account callback cannot request the next account host list');
  view.value.selectNode('mac-b');
  assert.equal(view.value.activeNode.id, 'mac-b');
  view.dispose();
});

test('selected hosts are restored per account and unavailable saved hosts fall back safely', async () => {
  const storage = new Map([
    ['connect-bots:account:account-a:node', 'mac-a'],
    ['connect-bots:account:account-b:node', 'removed-b'],
  ]);
  const view = fixture(session('account-a'), storage);
  await view.flush();
  assert.equal(view.value.activeNodeId, 'mac-a');
  assert.equal(view.sources[0].url, '/api/studio/events?after=0&expectedAccount=account-a&node=mac-a');
  view.requests.nodes[0].resolve({ nodes: [node('local'), node('mac-a')] });
  await view.flush();
  await view.value.logout();
  await view.value.login({ username: 'account-b', password: 'password' });
  await view.flush();
  assert.equal(view.value.activeNodeId, 'removed-b');
  view.requests.nodes[1].resolve({ nodes: [node('local')] });
  await view.flush();
  assert.equal(view.value.activeNodeId, 'local');
  assert.equal(storage.get('connect-bots:account:account-a:node'), 'mac-a');
  assert.equal(storage.get('connect-bots:account:account-b:node'), 'local');
  view.dispose();
});

test('a late bootstrap session cannot replace a newly authenticated account', async () => {
  const pending = deferred();
  const view = fixture(pending.promise);
  await view.value.login({ username: 'account-b', password: 'password' });
  await view.flush();
  pending.resolve(session('account-a'));
  await view.flush();
  assert.equal(view.value.user.id, 'account-b');
  assert.equal(view.value.phase, 'ready');
  assert.equal(view.sources.length, 1);
  view.dispose();
});

test('switching accounts clears the cursor and ignores old requests, stream events, and retained callbacks', async () => {
  const view = fixture();
  await view.flush();
  const oldSource = view.sources[0];
  oldSource.emit(event(99));
  view.requests.bots[0].resolve({ bots: [bot('shared', 'Account A bot')] });
  await view.flush();
  view.value.selectCatalogBot('shared');
  await view.flush();
  const oldCallbacks = view.value;
  const history = oldCallbacks.loadHistory('shared');
  const oldCatalog = view.requests.capabilities[0];
  await oldCallbacks.logout();
  await view.flush();
  assert.equal(view.value.user, null);
  assert.equal(view.value.bots.length, 0);
  assert.equal(Object.keys(view.value.events).length, 0);
  assert.equal(oldSource.closed, true);
  await view.value.login({ username: 'account-b', password: 'password' });
  await view.flush();
  assert.equal(view.sources[1].url, '/api/studio/events?after=0&expectedAccount=account-b&node=local');
  view.requests.bots[1].resolve({ bots: [bot('new-bot', 'Account B bot')] });
  oldCatalog.resolve({ models: [{ model: 'account-a-secret' }] });
  view.requests.history[0].resolve({ events: [event(100)] });
  await history;
  oldSource.emit({ ...event(101), type: 'bot', data: { bot: bot('leaked', 'Private bot') } });
  oldCallbacks.updateBot(bot('leaked-callback', 'Private callback'));
  oldCallbacks.archiveBot('new-bot');
  oldCallbacks.setError('Private old account error');
  await view.flush();
  assert.deepEqual(Array.from(view.value.bots, bot => bot.name), ['Account B bot']);
  assert.equal(Object.keys(view.value.events).length, 0);
  assert.equal(view.value.capabilities, null);
  assert.equal(view.value.error, '');
  view.dispose();
});

test('SSE events wait for matching account identity and a changed cookie remounts the new account', async () => {
  const view = fixture();
  await view.flush();
  const source = view.sources[0];
  const verification = deferred();
  view.sessionQueue.push(verification.promise);
  source.open();
  source.emit(event(7));
  assert.equal(Object.keys(view.value.events).length, 0, 'unverified events remain outside rendered history');
  verification.resolve(session('account-a'));
  await view.flush();
  assert.equal(view.value.events.shared[0].seq, 7);
  view.setCookie(session('account-b'));
  source.fail();
  source.open();
  source.emit(event(8));
  await view.flush();
  assert.equal(view.value.user.id, 'account-b');
  assert.equal(Object.keys(view.value.events).length, 0);
  assert.equal(source.closed, true);
  assert.equal(view.sources[1].url, '/api/studio/events?after=0&expectedAccount=account-b&node=local');
  view.dispose();
});

test('an expired history request clears all account-owned state and closes its stream', async () => {
  const view = fixture();
  await view.flush();
  view.requests.bots[0].resolve({ bots: [bot('shared', 'Private bot')] });
  await view.flush();
  const request = view.value.loadHistory('shared');
  view.requests.history[0].reject(new ApiError('unauthorized', 401));
  await request;
  await view.flush();
  assert.equal(view.value.phase, 'login');
  assert.equal(view.value.user, null);
  assert.equal(view.value.loaded, false);
  assert.equal(view.value.bots.length, 0);
  assert.equal(view.value.capabilities, null);
  assert.equal(view.sources[0].closed, true);
  assert.match(view.value.error, /expired/);
  view.dispose();
});

test('tenant bot and capability requests wait for identity verification after a cookie change', async () => {
  const view = fixture();
  const verification = deferred();
  view.sessionQueue.push(verification.promise);
  await view.flush();
  assert.equal(view.value.user.id, 'account-a');
  assert.equal(view.requests.bots.length, 0, 'an unverified cookie cannot fetch account-owned bots');
  assert.equal(view.requests.capabilities.length, 0);
  view.sources[0].emit({ ...event(55), type: 'bot', data: { bot: bot('secret-b', 'B bot') } });
  assert.equal(view.value.bots.length, 0);
  view.setCookie(session('account-b'));
  verification.resolve(session('account-b'));
  await view.flush();
  assert.equal(view.value.user.id, 'account-b');
  assert.equal(view.value.bots.length, 0, 'the unverified prior stream is discarded');
  assert.equal(view.requests.bots.length, 1, 'only the verified new account starts its bot request');
  view.requests.bots[0].resolve({ bots: [] });
  await view.flush();
  assert.equal(view.requests.capabilities.length, 1);
  view.dispose();
});

test('a probe from an earlier SSE connection cannot verify a reconnect with another account cookie', async () => {
  const view = fixture();
  await view.flush();
  const oldProbe = deferred();
  const newProbe = deferred();
  view.sessionQueue.push(oldProbe.promise, newProbe.promise);
  const source = view.sources[0];
  source.open();
  view.setCookie(session('account-b'));
  source.fail();
  source.open();
  source.emit({ ...event(50), type: 'bot', data: { bot: bot('secret-b', 'B private bot') } });
  oldProbe.resolve(session('account-a'));
  await view.flush();
  assert.equal(view.value.user.id, 'account-a');
  assert.equal(view.value.bots.length, 0, 'a stale A probe cannot publish B events');
  assert.equal(view.requests.bots.length, 1, 'the new connection stays blocked on its own identity probe');
  newProbe.resolve(session('account-b'));
  await view.flush();
  assert.equal(view.value.user.id, 'account-b');
  assert.equal(view.value.bots.length, 0);
  assert.equal(source.closed, true);
  assert.equal(view.sources[1].url, '/api/studio/events?after=0&expectedAccount=account-b&node=local');
  view.dispose();
});

test('sign-out keeps the login form in its checking phase until the cookie is cleared', async () => {
  const view = fixture();
  await view.flush();
  const pending = deferred();
  view.logoutQueue.push(pending.promise);
  const logout = view.value.logout();
  assert.equal(view.value.phase, 'checking');
  assert.equal(view.value.user, null);
  assert.equal(view.value.bots.length, 0);
  assert.equal(view.requests.logout[0], 'account-a', 'sign-out retains its expected account after clearing local state');
  pending.resolve();
  await logout;
  await view.flush();
  assert.equal(view.value.phase, 'login');
  view.dispose();
});

test('a global account_changed response clears the old workspace and rechecks identity without replay', async () => {
  const view = fixture();
  await view.flush();
  view.requests.bots[0].resolve({ bots: [bot('private-a', 'A private bot')] });
  await view.flush();
  view.setCookie(session('account-b'));
  view.changed('account-a');
  assert.equal(view.value.phase, 'checking');
  assert.equal(view.value.user, null);
  assert.equal(view.value.bots.length, 0);
  assert.equal(view.sources[0].closed, true);
  await view.flush();
  assert.equal(view.value.user.id, 'account-b');
  assert.equal(view.value.phase, 'ready');
  assert.equal(view.sources[1].url, '/api/studio/events?after=0&expectedAccount=account-b&node=local');
  assert.equal(view.requests.login.length, 0);
  assert.equal(view.requests.register.length, 0);
  view.dispose();
});

test('stale account logout sends its original identity and opens the changed session without revoking it', async () => {
  const view = fixture();
  await view.flush();
  view.setCookie(session('account-b'));
  const rejected = deferred();
  view.logoutQueue.push(rejected.promise);
  const logout = view.value.logout();
  rejected.reject(new ApiError('Account session changed.', 409, 'account_changed'));
  await logout;
  await view.flush();
  assert.equal(view.requests.logout[0], 'account-a');
  assert.equal(view.value.phase, 'ready');
  assert.equal(view.value.user.id, 'account-b');
  assert.equal(view.requests.logout.length, 1);
  view.dispose();
});

test('an old reconnect probe cannot expire a freshly verified connection', async () => {
  const view = fixture();
  await view.flush();
  const pending = deferred();
  view.sessionQueue.push(pending.promise);
  const probe = view.probes[0].options;
  const checking = probe.check();
  view.sources[0].open();
  await view.flush();
  pending.resolve({ authenticated: false, registrationAllowed: true });
  const staleSession = await checking;
  if (!staleSession.authenticated) probe.expired();
  await view.flush();
  assert.equal(view.value.phase, 'ready');
  assert.equal(view.value.user.id, 'account-a');
  view.dispose();
});

test('registration enters the returned account immediately and authenticated sessions require an identity', async () => {
  const view = fixture({ authenticated: false, registrationAllowed: true, setupRequired: true });
  await view.flush();
  assert.equal(view.value.setupRequired, true);
  await view.value.register({ username: 'new.user', password: 'password' });
  await view.flush();
  assert.equal(view.value.phase, 'ready');
  assert.equal(view.value.user.username, 'new.user');
  assert.equal(view.value.loaded, false);
  view.dispose();
  const invalid = fixture({ authenticated: true, registrationAllowed: true });
  await invalid.flush();
  assert.equal(invalid.value.phase, 'login');
  assert.equal(invalid.value.user, null);
  assert.match(invalid.value.error, /invalid session/);
  assert.equal(invalid.sources.length, 0);
  invalid.dispose();
});

test('token bursts publish once, completion flushes in order, and roster previews do not rerender', async () => {
  const view = fixture(); await view.flush();
  view.requests.bots[0].resolve({ bots: [bot('shared', 'Assistant')] }); await view.flush();
  const before = view.value;
  const delta = seq => ({ ...event(seq), type: 'native', data: { method: 'item/agentMessage/delta', params: { delta: 'a' } } });
  const timers = new Set(view.timers.keys());
  for (let seq = 1; seq <= 100; seq++) view.sources[0].emit(delta(seq));
  assert.equal(view.value.events, before.events, 'deltas wait for a single bounded flush');
  assert.equal([...view.timers.keys()].filter(id => !timers.has(id)).length, 1);
  view.sources[0].emit({ ...event(101), type: 'turn', data: { status: 'completed' } });
  assert.deepEqual(Array.from(view.value.events.shared, e => e.seq), Array.from({ length: 101 }, (_, i) => i + 1));
  assert.equal(view.value.rosterEvents, before.rosterEvents, 'tokens and tool events cannot wake the roster');
  assert.equal(view.value.bots, before.bots);
  assert.equal(view.value.catalogBots, before.catalogBots);
  assert.equal([...view.timers.keys()].filter(id => !timers.has(id)).length, 0);
  view.sources[0].emit(event(102));
  assert.notEqual(view.value.rosterEvents, before.rosterEvents, 'a real message refreshes the preview');
  view.dispose();
});

test('scheduled token batches flush while running and are cancelled on account exit', async () => {
  const view = fixture(); await view.flush();
  const before = new Set(view.timers.keys());
  const delta = seq => ({ ...event(seq), type: 'native', data: { method: 'item/agentMessage/delta' } });
  view.sources[0].emit(delta(1));
  const timer = [...view.timers].find(([id]) => !before.has(id));
  timer[1]();
  assert.equal(view.value.events.shared.length, 1);
  view.sources[0].emit(delta(2));
  const delayed = [...view.timers].find(([id]) => !before.has(id));
  const logout = view.value.logout(); await view.flush(); await logout;
  delayed?.[1]();
  assert.equal(Object.keys(view.value.events).length, 0, 'an old flush never restores signed-out history');
  assert.equal(Object.keys(view.value.rosterEvents).length, 0);
  view.dispose();
});

test('chat history loads once per workspace, shares in-flight requests, and explicit refresh still works', async () => {
  const view = fixture(); await view.flush();
  const first = view.value.loadHistory('shared');
  const second = view.value.loadHistory('shared');
  assert.equal(first, second);
  assert.equal(view.requests.history.length, 1);
  view.requests.history[0].resolve({ events: [event(1)] }); await first; await view.flush();
  const history = view.value.events.shared;
  await view.value.loadHistory('shared');
  assert.equal(view.requests.history.length, 1, 'switching back does not download the full journal again');
  assert.equal(view.value.events.shared, history);
  const refresh = view.value.loadHistory('shared', true);
  assert.equal(view.requests.history.length, 2);
  view.requests.history[1].reject(new Error('temporary outage'));
  await assert.rejects(refresh, /temporary outage/);
  const retry = view.value.loadHistory('shared');
  assert.equal(view.requests.history.length, 3, 'failed refresh never poisons the cache');
  view.requests.history[2].resolve({ events: [event(1), event(2)] }); await retry;
  view.dispose();
});
