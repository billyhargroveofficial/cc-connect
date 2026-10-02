import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { contextNumbers, contextCompacting, contextEventRevision } from '../../lib/contextState.ts';
import { motionTestModule } from '../../lib/motion-stub.mjs';

const source = path => ts.transpileModule(readFileSync(new URL(path, import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const hookSource = source('../../hooks/useBotContext.ts');
const controlSource = source('./ContextControl.tsx');
const settled = () => new Promise(resolve => setImmediate(resolve));
function deferred() {
  let resolve, reject;
  const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

// The actual context hook, API action and leaf are driven together. Only React
// plumbing and timers are stubbed; request locking and native progress are real.
function contextControl({ backend = 'codex', context = { compacting: false, usedTokens: 25000, contextWindow: 100000 }, busy = false } = {}) {
  let cursor = 0, effectCursor = 0, nextTimer = 0, present = true;
  let snapshot = context, snapshotGate, compactGate;
  const values = [], effects = [], pendingEffects = [], timers = new Map(), calls = [], errors = [];
  const state = initial => { const index = cursor++; if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial; return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }]; };
  const memo = (factory, dependencies) => { const index = cursor++; const old = values[index]; if (!old || dependencies.some((value, offset) => !Object.is(value, old.dependencies[offset]))) values[index] = { dependencies, value: factory() }; return values[index].value; };
  const element = (type, props) => ({ type, props });
  const react = {
    useState: state, useRef: initial => state(() => ({ current: initial }))[0], useMemo: memo,
    useCallback: (callback, dependencies) => memo(() => callback, dependencies),
    useEffect(callback, dependencies) {
      const index = effectCursor++, old = effects[index];
      if (!old || dependencies.some((value, offset) => !Object.is(value, old.dependencies[offset]))) pendingEffects.push(() => {
        old?.cleanup?.(); effects[index] = { dependencies, callback, cleanup: callback() };
      });
    },
  };
  const api = {
    context: async () => snapshotGate ? snapshotGate.promise : snapshot,
    compact: async id => { calls.push(id); return compactGate ? compactGate.promise : { requestId: 'compact-1' }; },
  };
  const modules = {
    react, 'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../lib/api': { api }, '../../lib/api': { errorMessage: error => error.message },
    '../lib/contextState': { contextCompacting, contextEventRevision }, '../../lib/contextState': { contextNumbers },
    '../../lib/motion': { ...motionTestModule(), useIsPresent: () => present },
  };
  function load(code, filename) {
    const exports = {};
    runInNewContext(code, { exports, require: name => { assert.ok(name in modules, `Unexpected import ${name}`); return modules[name]; }, AbortController,
      setTimeout: callback => { const id = ++nextTimer; timers.set(id, callback); return id; }, clearTimeout: id => timers.delete(id),
    }, { filename });
    return exports;
  }
  const hook = load(hookSource, 'useBotContext.ts').useBotContext;
  const Control = load(controlSource, 'ContextControl.tsx').default;
  const props = { busy, offline: false, suspended: false, onError: error => errors.push(error) };
  const bot = { id: 'bot', backend, model: 'model', threads: { [backend]: 'root' } };
  let events = [], tree, runtime;
  function render() {
    cursor = 0; effectCursor = 0;
    runtime = hook(bot, events); tree = Control({ ...props, state: runtime });
    for (const effect of pendingEffects.splice(0)) effect();
    return tree;
  }
  function all(match) {
    render(); const nodes = [];
    function visit(node) { if (!node || typeof node !== 'object') return; if (match(node)) nodes.push(node); const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children]; children.flat(Infinity).forEach(visit); }
    visit(tree); return nodes;
  }
  render();
  return {
    props, calls, errors, render,
    button: () => all(node => node.type === 'button')[0],
    spinner: () => all(node => node.type === 'LoaderCircle' && node.props.className === 'spin')[0],
    usage: () => all(node => node.type === 'b')[0].props.children,
    compact: () => runtime.compact(),
    snapshot: value => { snapshot = value; },
    gateSnapshot: gate => { snapshotGate = gate; },
    gateCompact: gate => { compactGate = gate; },
    events: next => { events = next; render(); },
    exit: () => { present = false; render(); },
    replayEffects: () => { for (const effect of effects) { effect?.cleanup?.(); if (effect) effect.cleanup = effect.callback(); } },
    unmount: () => { for (const effect of effects) effect?.cleanup?.(); },
    flush: async () => { const scheduled = [...timers.values()]; timers.clear(); scheduled.forEach(callback => callback()); await settled(); render(); },
  };
}
const action = (seq, status) => ({ seq, botId: 'bot', type: 'compact_action', data: { backend: 'codex', threadId: 'root', status, requestId: 'compact-1' } });

test('statusline context click requests native compaction once and keeps its percentage and spinner through the accepted receipt', async () => {
  const view = contextControl();
  assert.equal(view.button().props.disabled, true, 'context must be loaded first');
  await view.flush();
  const button = view.button();
  assert.equal(button.props['aria-label'], 'Compact context · Context 25%');
  assert.equal(button.props.disabled, false);
  assert.equal(view.usage(), '25%');
  const post = deferred(); view.gateCompact(post);
  button.props.onClick(); button.props.onClick();
  assert.deepEqual(view.calls, ['bot'], 'double-click before commit cannot POST twice');
  assert.equal(view.button().props.disabled, true);
  assert.equal(view.button().props['aria-busy'], true);
  assert.match(view.button().props['aria-label'], /Requesting compaction/);
  assert.ok(view.spinner()); assert.equal(view.usage(), '25%');
  const refresh = deferred(); view.gateSnapshot(refresh);
  post.resolve({ requestId: 'compact-1' }); await settled();
  assert.match(view.button().props['aria-label'], /Compacting context/);
  assert.ok(view.spinner(), 'receipt preserves progress before SSE/GET arrives');
  await view.compact(); assert.equal(view.calls.length, 1);
  view.events([action(1, 'started')]);
  await view.flush(); refresh.resolve({ compacting: true, usedTokens: 25000, contextWindow: 100000 }); await settled();
  assert.equal(view.button().props.disabled, true); assert.ok(view.spinner());
  view.events([action(1, 'started'), action(2, 'completed')]);
  assert.equal(view.spinner(), undefined, 'tracked native completion ends progress');
  assert.equal(view.button().props.disabled, false);
  view.unmount();
});

test('unsupported providers retain read-only context and never submit manual compaction', async () => {
  const view = contextControl({ backend: 'other', context: { compacting: false, percent: 12.5, estimated: true } });
  await view.flush();
  assert.equal(view.button(), undefined); assert.equal(view.usage(), '≈13%');
  await view.compact(); assert.deepEqual(view.calls, []);
  view.unmount();
});

test('native statusline control stays disabled during a turn, offline, suspended and animated exit', async () => {
  for (const block of [view => { view.props.busy = true; }, view => { view.props.offline = true; }, view => { view.props.suspended = true; }, view => view.exit()]) {
    const view = contextControl(); await view.flush(); block(view);
    assert.equal(view.button().props.disabled, true);
    view.button().props.onClick(); assert.deepEqual(view.calls, []);
    view.unmount();
  }
});

test('a failed compaction request reports the error and releases the statusline for retry', async () => {
  const view = contextControl({ backend: 'pi', context: { compacting: false, usedTokens: 0, contextWindow: 100000 } });
  await view.flush(); const post = deferred(); view.gateCompact(post);
  view.button().props.onClick(); post.reject(new Error('Native compaction failed')); await settled();
  assert.deepEqual(view.errors, ['Native compaction failed']);
  assert.equal(view.spinner(), undefined); assert.equal(view.button().props.disabled, false); assert.equal(view.usage(), '0%');
  view.unmount();
});

test('a delayed context refresh cannot overwrite the terminal compaction event', async () => {
  const view = contextControl(); await view.flush();
  view.button().props.onClick(); await settled();
  const refresh = deferred(); view.gateSnapshot(refresh); await view.flush();
  view.events([action(1, 'started'), action(2, 'failed')]);
  refresh.resolve({ compacting: true, usedTokens: 25000, contextWindow: 100000 }); await settled();
  assert.equal(view.spinner(), undefined); assert.equal(view.button().props.disabled, false);
  view.unmount();
});

test('a context read started before the accepted request cannot clear its compaction gate', async () => {
  const view = contextControl(); await view.flush();
  const stale = deferred(); view.gateSnapshot(stale);
  view.events([{ seq: 1, botId: 'bot', type: 'turn', data: { status: 'completed' } }]);
  await view.flush();
  view.button().props.onClick(); await settled();
  // Resolve before the next render/effect cleanup to exercise the in-flight
  // read, not merely AbortController's ordinary cancellation path.
  stale.resolve({ compacting: false, usedTokens: 25000, contextWindow: 100000 }); await settled();
  assert.ok(view.spinner()); assert.equal(view.button().props.disabled, true);
  view.unmount();
});

test('the current context button remains active when React replays mount effects', async () => {
  const view = contextControl(); await view.flush();
  const trigger = view.button().props.onClick;
  view.replayEffects(); trigger(); await settled();
  assert.deepEqual(view.calls, ['bot']);
  view.unmount();
});
