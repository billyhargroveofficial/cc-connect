import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from './lib/motion-stub.mjs';
import * as hostCatalog from './lib/hostCatalog.ts';

const source = ts.transpileModule(`${readFileSync(new URL('./App.tsx', import.meta.url), 'utf8')}\nexport { AccountWorkspace, ConversationPane };`, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function fixture({ root = false, conversation = false, loadHistory = async () => {} } = {}) {
  const slots = [];
  let index = 0, pendingEffects = [], present = true, dirty = false, tree;
  let lazyId = 0;
  const listeners = new Map();
  const location = { hash: '#/bots/bot-a', pathname: '/', search: '' };
  const changes = [];
  const routes = [], nodeSelections = [], historyReplacements = [], historyLoads = [], errors = [];
  let conversationKey;
  const emptyEvents = [];
  const react = {
    useState(initial) {
      const i = index++;
      if (!(i in slots)) slots[i] = { value: typeof initial === 'function' ? initial() : initial };
      return [slots[i].value, next => {
        const result = typeof next === 'function' ? next(slots[i].value) : next;
        if (!Object.is(result, slots[i].value)) { slots[i].value = result; dirty = true; }
      }];
    },
    useRef(value) { const i = index++; return slots[i] ??= { current: value }; },
    useCallback(callback, deps) {
      const i = index++;
      if (!slots[i] || deps.some((value, j) => !Object.is(value, slots[i].deps[j]))) slots[i] = { deps, callback };
      return slots[i].callback;
    },
    useEffect(effect, deps) {
      const i = index++;
      if (!slots[i] || deps.some((value, j) => !Object.is(value, slots[i].deps[j]))) {
        const old = slots[i];
        const slot = { effect, deps };
        slots[i] = slot;
        pendingEffects.push(() => { old?.cleanup?.(); slot.cleanup = effect(); });
      }
    },
    useLayoutEffect(effect, deps) { react.useEffect(effect, deps); },
    memo: component => component,
    lazy: () => `Lazy${++lazyId}`,
    Suspense: 'Suspense',
  };
  const element = (type, props, key) => {
    if (type === exports.ConversationPane) conversationKey = key;
    return root && type === exports.AccountWorkspace
      ? { type: 'AccountWorkspace', props, key }
      : typeof type === 'function' ? type(props) : { type, props, key };
  };
  const modules = {
    react, 'react/jsx-runtime': { jsx: element, jsxs: element },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    './hooks/useWorkspace': { useWorkspace: () => workspace },
    './hooks/useTheme': { useTheme: () => ({ preference: 'system', setPreference() {} }) },
    './hooks/useEventJournal': {
      useBotEvents: (_journal, id) => workspace.events[id] || emptyEvents,
      useRosterEvents: () => workspace.rosterEvents,
    },
    './lib/api': { errorMessage: error => error.message },
    './lib/hostCatalog': hostCatalog,
    './components/Login': { default: 'Login' },
    './components/Avatar': { default: 'Avatar' },
    './components/BotRoster': { default: 'BotRoster' },
    './lib/motion': { ...motionTestModule(), useIsPresent: () => present },
  };
  const exports = {};
  runInNewContext(source, {
    exports, location,
    history: { replaceState: (...args) => {
      historyReplacements.push(args);
      location.hash = String(args[2]).includes('#') ? `#${String(args[2]).split('#')[1]}` : '';
    } },
    sessionStorage: { getItem: () => null, setItem() {} },
    window: {
      matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
      addEventListener: (name, handler) => listeners.set(name, handler),
      removeEventListener: (name, handler) => { if (listeners.get(name) === handler) listeners.delete(name); },
    },
    require: name => modules[name],
  }, { filename: 'App.tsx' });
  const workspace = {
    phase: 'ready', user: { id: 'account-a', username: 'alice' }, accountVersion: 1, workspaceVersion: 1,
    activeNodeId: 'local', activeNode: { id: 'local', name: 'This server', online: true, local: true },
    nodes: [{ id: 'local', name: 'This server', online: true, local: true }],
    bots: [{ id: 'bot-a', chief: false }], allBots: [], events: {}, rosterEvents: {}, capabilities: null,
    catalogBots: [], hostFilters: { server: true, mac: true }, setHostFilter() {},
    loaded: true, error: '', connection: 'connected',
    eventJournal: { messagesFor: id => workspace.events[id]?.filter(event => event.type === 'message') || emptyEvents },
    updateBot: bot => changes.push(bot), archiveBot() {}, selectCatalogBot() {},
    loadHistory: (id, force) => {
      historyLoads.push({ nodeId: workspace.activeNodeId, id, force });
      return loadHistory(id, force);
    },
    logout: async () => {}, setError: message => errors.push(message), selectNode: id => nodeSelections.push(id), refreshNodes: async () => [],
  };
  const routeChanged = (nodeId, id) => routes.push([nodeId, id]);
  const setTheme = () => {};
  function render() {
    do {
      index = 0; pendingEffects = []; dirty = false;
      tree = root ? exports.default()
        : conversation ? exports.ConversationPane({
          journal: workspace.eventJournal, loadHistory: workspace.loadHistory, onError: workspace.setError,
          bot: workspace.bots[0], node: workspace.activeNode,
          draftScope: `${workspace.user.id}:${workspace.activeNodeId}`,
        })
        : exports.AccountWorkspace({ workspace, initialBot: 'bot-a', onRouteChange: routeChanged, theme: 'system', setTheme });
      for (const effect of pendingEffects) effect();
    } while (dirty);
    return tree;
  }
  function find(match) {
    render();
    function visit(node) {
      if (!node || typeof node !== 'object') return;
      if (match(node)) return node;
      for (const child of [node.props?.children].flat(Infinity)) { const found = visit(child); if (found) return found; }
    }
    return visit(tree);
  }
  render();
  return {
    location, changes, find, workspace, routes, nodeSelections, historyReplacements, historyLoads, errors,
    get conversationKey() { return conversationKey; },
    createCallback() { find(node => node.type === 'BotRoster').props.onCreate(); return find(node => node.props?.onCreated).props.onCreated; },
    exit() { present = false; render(); },
    dispose() { for (const slot of slots) slot?.cleanup?.(); },
    replayEffects() { for (const slot of slots) if (slot?.effect) { slot.cleanup?.(); slot.cleanup = slot.effect(); } },
  };
}

test('idle device polling, streamed events and reconnect status keep the active workspace mounted', () => {
  const view = fixture({ root: true });
  const before = view.find(node => node.type === 'AccountWorkspace');
  view.workspace.nodes = view.workspace.nodes.map(node => ({ ...node, lastSeenAt: '2026-10-02T13:00:00Z' }));
  view.workspace.catalogBots = [{ key: 'new-catalog-row', bot: { id: 'other-bot' }, node: view.workspace.nodes[0] }];
  view.workspace.events = { 'bot-a': [{ seq: 42, type: 'message', data: { text: 'New token' } }] };
  view.workspace.connection = 'reconnecting';
  const next = view.find(node => node.type === 'AccountWorkspace');
  assert.equal(next.key, before.key, 'React keeps ChatRoom and Composer under the same workspace instance');
  assert.equal(next.props.initialBot, 'bot-a');
  assert.equal(view.location.hash, '#/bots/bot-a');
  assert.equal(view.historyReplacements.length, 0);
  view.dispose();
});

test('a roster-selected conversation survives its host change and same-workspace effect replays', () => {
  const view = fixture({ root: true });
  view.find(node => node.type === 'AccountWorkspace').props.onRouteChange('mac-a', 'mac-bot');
  view.location.hash = '#/bots/mac-bot';
  view.workspace.activeNodeId = 'mac-a';
  view.workspace.workspaceVersion++;
  const next = view.find(node => node.type === 'AccountWorkspace');
  assert.equal(next.props.initialBot, 'mac-bot');
  assert.equal(view.location.hash, '#/bots/mac-bot');
  view.replayEffects();
  assert.equal(view.find(node => node.type === 'AccountWorkspace').props.initialBot, 'mac-bot');
  assert.equal(view.location.hash, '#/bots/mac-bot');
  assert.equal(view.historyReplacements.length, 0);
  view.dispose();
});

test('same-device chat selection only updates the hash and selected conversation', () => {
  const view = fixture();
  view.workspace.bots.push({ id: 'bot-b', chief: false });
  Object.defineProperty(view.location, 'href', { set() { assert.fail('Chat selection must not navigate the document'); } });
  view.location.reload = () => assert.fail('Chat selection must not reload the document');
  view.find(node => node.type === 'BotRoster').props.onSelect('bot-b', 'local');
  assert.equal(view.location.hash, '/bots/bot-b');
  assert.equal(view.find(node => node.type === 'Lazy1').props.bot.id, 'bot-b');
  assert.deepEqual(view.routes, [['local', 'bot-b']]);
  assert.deepEqual(view.nodeSelections, []);
  view.dispose();
});

test('a late bot-creation acknowledgement cannot navigate another account after exit or unmount', () => {
  for (const action of ['exit', 'dispose']) {
    const view = fixture();
    const created = view.createCallback();
    view[action]();
    view.location.hash = '#/bots/account-b-bot';
    created({ id: 'late-a-bot', name: 'Private A bot' });
    assert.equal(view.changes.length, 0);
    assert.equal(view.location.hash, '#/bots/account-b-bot');
  }
});

test('current bot creation still navigates after React StrictMode replays effects', () => {
  const view = fixture();
  const created = view.createCallback();
  view.replayEffects();
  created({ id: 'new-bot', name: 'New bot' });
  assert.equal(view.changes.length, 1);
  assert.equal(view.location.hash, '/bots/new-bot');
  view.dispose();
});

test('selecting a duplicate bot on another device routes and switches with the explicit host', () => {
  const view = fixture();
  view.workspace.nodes.push({ id: 'mac-a', name: 'MacBook', online: true, local: false });
  const roster = view.find(node => node.type === 'BotRoster');
  assert.equal(roster.props.selectedKey, hostCatalog.catalogBotKey('local', 'bot-a'));
  assert.equal(roster.props.bots, view.workspace.catalogBots);
  assert.equal(roster.props.hostFilters, view.workspace.hostFilters);
  assert.equal(roster.props.onFilterChange, view.workspace.setHostFilter);
  roster.props.onSelect('bot-a', 'mac-a');
  assert.deepEqual(view.routes, [['mac-a', 'bot-a']]);
  assert.deepEqual(view.nodeSelections, ['mac-a']);
  assert.equal(view.location.hash, '/bots/bot-a');
  assert.equal(view.changes.length, 0, 'another host selection does not rewrite the active host bot');
  view.dispose();
});

test('equal bot IDs on different nodes get separate conversation lifecycles and draft scopes without navigation', () => {
  const view = fixture();
  const local = view.find(node => node.type === 'Lazy1').props;
  const localKey = view.conversationKey;
  const mac = { id: 'mac-a', name: 'MacBook', online: true, local: false };
  view.workspace.nodes.push(mac);
  Object.defineProperty(view.location, 'href', { set() { assert.fail('A node switch must not navigate the document'); } });
  view.location.reload = () => assert.fail('A node switch must not reload the document');
  view.find(node => node.type === 'BotRoster').props.onSelect('bot-a', mac.id);
  view.workspace.activeNodeId = mac.id;
  view.workspace.activeNode = mac;
  view.workspace.bots = [{ id: 'bot-a', chief: false, status: 'running' }];
  const remoteEvents = [{ seq: 1, type: 'message', data: { text: 'From the Mac' } }];
  view.workspace.events = { 'bot-a': remoteEvents };
  const remote = view.find(node => node.type === 'Lazy1').props;
  assert.notEqual(view.conversationKey, localKey, 'React resets turn, queue, context and scroll state for the other host');
  assert.equal(remote.bot, view.workspace.bots[0]);
  assert.equal(remote.node, mac);
  assert.equal(remote.events, remoteEvents);
  assert.notEqual(remote.draftScope, local.draftScope, 'A Mac draft cannot overwrite the server draft');
  assert.deepEqual(view.historyLoads.map(({ nodeId, id }) => [nodeId, id]), [['local', 'bot-a'], ['mac-a', 'bot-a']]);
  assert.deepEqual(view.routes, [['mac-a', 'bot-a']]);
  assert.deepEqual(view.historyReplacements, []);

  view.workspace.activeNodeId = 'local';
  view.workspace.activeNode = view.workspace.nodes[0];
  view.workspace.bots = [local.bot];
  const restored = view.find(node => node.type === 'Lazy1').props;
  assert.equal(view.conversationKey, localKey);
  assert.equal(restored.draftScope, local.draftScope);
  const serverKey = view.conversationKey;
  view.workspace.user = { id: 'account-b', username: 'bob' };
  const otherAccount = view.find(node => node.type === 'Lazy1').props;
  assert.notEqual(view.conversationKey, serverKey, 'The same host and bot cannot reuse another account conversation');
  assert.notEqual(otherAccount.draftScope, local.draftScope);
  view.dispose();
});

test('same-ID node switches load the new history and ignore the previous host history completion', async () => {
  const requests = [];
  const view = fixture({ conversation: true, loadHistory: () => new Promise((resolve, reject) => requests.push({ resolve, reject })) });
  assert.equal(view.find(node => node.type === 'Lazy1').props.loading, true);
  view.workspace.activeNodeId = 'mac-a';
  view.workspace.activeNode = { id: 'mac-a', name: 'MacBook', online: true, local: false };
  const remoteEvents = [{ seq: 1, type: 'message', data: { text: 'Mac history' } }];
  view.workspace.events = { 'bot-a': remoteEvents };
  const remote = view.find(node => node.type === 'Lazy1').props;
  assert.equal(remote.events, remoteEvents);
  assert.equal(remote.loading, true);
  assert.equal(requests.length, 2, 'Even a stable loader and equal bot ID must load the new node');
  requests[0].reject(new Error('The old server request failed'));
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(view.errors, [], 'An exiting host history request cannot show a notification in the new host');
  assert.equal(view.find(node => node.type === 'Lazy1').props.loading, true, 'An old completion cannot dismiss the new host loading state');
  requests[1].resolve();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(view.find(node => node.type === 'Lazy1').props.loading, false);
  view.dispose();
});

test('conversation history follows a replaced journal but ignores idle node metadata changes', () => {
  const view = fixture({ conversation: true });
  const initialLoads = view.historyLoads.length;
  view.workspace.activeNode = { ...view.workspace.activeNode, lastSeenAt: '2026-10-02T13:00:00Z' };
  view.find(node => node.type === 'Lazy1');
  assert.equal(view.historyLoads.length, initialLoads);
  const messages = [{ seq: 2, type: 'message', data: { text: 'New journal' } }];
  view.workspace.eventJournal = { messagesFor: () => messages };
  assert.equal(view.find(node => node.type === 'Lazy1').props.messages, messages);
  assert.equal(view.historyLoads.length, initialLoads + 1, 'A cleared/replaced store must load its own history');
  view.dispose();
});

test('removed-device or exiting-roster selections cannot change another workspace route', () => {
  const view = fixture();
  const roster = view.find(node => node.type === 'BotRoster');
  roster.props.onSelect('bot-a', 'removed-mac');
  assert.deepEqual(view.nodeSelections, []);
  assert.deepEqual(view.routes, []);
  view.workspace.nodes.push({ id: 'mac-a', name: 'MacBook', online: true, local: false });
  view.exit();
  roster.props.onSelect('bot-a', 'mac-a');
  assert.deepEqual(view.nodeSelections, []);
  assert.deepEqual(view.routes, []);
  view.dispose();
});

test('new-bot model and creation callbacks stay stable when streamed workspace events rerender App', () => {
  const view = fixture();
  view.find(node => node.type === 'BotRoster').props.onCreate();
  const first = view.find(node => node.props?.onCreated);
  view.workspace.events = { 'bot-a': [{ seq: 1, type: 'message', data: { content: 'Token delta' } }] };
  const next = view.find(node => node.props?.onCreated);
  assert.equal(next.props.loadCapabilities, first.props.loadCapabilities);
  assert.equal(next.props.createBot, first.props.createBot);
  assert.equal(next.props.onCreated, first.props.onCreated);
  view.dispose();
});

test('background bot tokens leave the visible chat and roster props referentially stable', () => {
  const view = fixture();
  const chat = view.find(node => node.type === 'Lazy1').props;
  const roster = view.find(node => node.type === 'BotRoster').props;
  view.workspace.events = { ...view.workspace.events, 'another-bot': [{ seq: 1, type: 'native', data: {} }] };
  const nextChat = view.find(node => node.type === 'Lazy1').props;
  const nextRoster = view.find(node => node.type === 'BotRoster').props;
  for (const key of Object.keys(chat)) assert.equal(nextChat[key], chat[key], `chat prop ${key} stays stable`);
  for (const key of Object.keys(roster)) assert.equal(nextRoster[key], roster[key], `roster prop ${key} stays stable`);
  view.dispose();
});
