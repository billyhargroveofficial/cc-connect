import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from './lib/motion-stub.mjs';

const source = ts.transpileModule(`${readFileSync(new URL('./App.tsx', import.meta.url), 'utf8')}\nexport { AccountWorkspace };`, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function fixture() {
  const slots = [];
  let index = 0, pendingEffects = [], present = true, dirty = false, tree;
  let lazyId = 0;
  const listeners = new Map();
  const location = { hash: '#/bots/bot-a' };
  const changes = [];
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
    useCallback(callback) { return callback; },
    useEffect(effect, deps) {
      const i = index++;
      if (!slots[i] || deps.some((value, j) => !Object.is(value, slots[i].deps[j]))) {
        const old = slots[i];
        const slot = { effect, deps };
        slots[i] = slot;
        pendingEffects.push(() => { old?.cleanup?.(); slot.cleanup = effect(); });
      }
    },
    useLayoutEffect() {},
    lazy: () => `Lazy${++lazyId}`,
    Suspense: 'Suspense',
  };
  const element = (type, props) => typeof type === 'function' ? type(props) : { type, props };
  const modules = {
    react, 'react/jsx-runtime': { jsx: element, jsxs: element },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    './hooks/useWorkspace': {}, './hooks/useTheme': {},
    './lib/api': { errorMessage: error => error.message },
    './components/Login': { default: 'Login' },
    './components/Avatar': { default: 'Avatar' },
    './components/BotRoster': { default: 'BotRoster' },
    './lib/motion': { ...motionTestModule(), useIsPresent: () => present },
  };
  const exports = {};
  runInNewContext(source, {
    exports, location,
    window: {
      matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
      addEventListener: (name, handler) => listeners.set(name, handler),
      removeEventListener: (name, handler) => { if (listeners.get(name) === handler) listeners.delete(name); },
    },
    require: name => modules[name],
  }, { filename: 'App.tsx' });
  const workspace = {
    user: { id: 'account-a', username: 'alice' }, accountVersion: 1,
    bots: [{ id: 'bot-a', chief: false }], allBots: [], events: {}, capabilities: null,
    loaded: true, error: '', connection: 'connected',
    updateBot: bot => changes.push(bot), archiveBot() {}, selectCatalogBot() {},
    loadHistory: async () => {}, logout: async () => {}, setError() {},
  };
  function render() {
    do {
      index = 0; pendingEffects = []; dirty = false;
      tree = exports.AccountWorkspace({ workspace, initialBot: 'bot-a', theme: 'system', setTheme() {} });
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
    location, changes, find,
    createCallback() { find(node => node.type === 'BotRoster').props.onCreate(); return find(node => node.props?.onCreated).props.onCreated; },
    exit() { present = false; render(); },
    dispose() { for (const slot of slots) slot?.cleanup?.(); },
    replayEffects() { for (const slot of slots) if (slot?.effect) { slot.cleanup?.(); slot.cleanup = slot.effect(); } },
  };
}

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
