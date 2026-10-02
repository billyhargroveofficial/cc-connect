import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';

const source = ts.transpileModule(readFileSync(new URL('./useWorkspace.ts', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText;
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

function fixture(initialSession = session('account-a')) {
  const slots = [];
  const sources = [];
  const probes = [];
  const requests = { bots: [], history: [], capabilities: [], login: [], register: [], logout: [] };
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
    requests[kind].push({ ...result, args });
    return result.promise;
  }
  const api = {
    session: async () => sessionQueue.length ? await sessionQueue.shift() : cookieSession,
    bots: () => pending('bots', []),
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
      setApiAccount() {},
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
  };
  const exports = {};
  runInNewContext(source, { exports, EventSource: FakeEventSource, AbortController, require: name => modules[name] }, { filename: 'useWorkspace.ts' });
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
    get value() { return render(); }, sources, probes, requests, sessionQueue, logoutQueue, render, flush,
    setCookie(next) { cookieSession = next; },
    changed(accountId) { for (const listener of accountListeners) listener(accountId); },
    dispose() { for (const slot of slots) slot?.cleanup?.(); },
  };
}

const event = (seq, botId = 'shared') => ({ seq, botId, type: 'message', time: '2026-10-02T10:00:00Z', data: { text: 'Private account history' } });

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
  assert.equal(view.sources[1].url, '/api/studio/events?after=0&expectedAccount=account-b');
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
  assert.equal(view.sources[1].url, '/api/studio/events?after=0&expectedAccount=account-b');
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
  assert.equal(view.sources[1].url, '/api/studio/events?after=0&expectedAccount=account-b');
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
  assert.equal(view.sources[1].url, '/api/studio/events?after=0&expectedAccount=account-b');
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
