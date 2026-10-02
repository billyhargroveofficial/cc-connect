import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';

const source = ts.transpileModule(readFileSync(new URL('./useMessageQueue.ts', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText;
const deferred = () => { let resolve, reject; const promise = new Promise((done, fail) => { resolve = done; reject = fail; }); return { promise, resolve, reject }; };
const settled = () => new Promise(resolve => setImmediate(resolve));
function queue({ scope = 'account-a:mac-a', snapshot = { messages: [], paused: false } } = {}) {
  let cursor = 0, effectCursor = 0, revision = 0, offline = false, gate, mutationError;
  const values = [], effects = [], pendingEffects = [], timers = [], calls = [], errors = [];
  const state = initial => { const index = cursor++; if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial; return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }]; };
  const memo = (factory, dependencies) => { const index = cursor++; const old = values[index]; if (!old || dependencies.some((value, offset) => value !== old.dependencies[offset])) values[index] = { dependencies, value: factory() }; return values[index].value; };
  const onError = error => errors.push(error);
  const modules = {
    react: { useState: state, useRef: initial => state(() => ({ current: initial }))[0], useMemo: memo, useCallback: (callback, dependencies) => memo(() => callback, dependencies),
      useEffect(callback, dependencies) { const index = effectCursor++; const old = effects[index]; if (!old || dependencies.some((value, offset) => value !== old.dependencies[offset])) pendingEffects.push(() => { old?.cleanup?.(); effects[index] = { dependencies, cleanup: callback() }; }); } },
    '../../lib/api': { errorMessage: error => error.message, api: {
      queue: async (botId, signal, binding) => { calls.push({ action: 'get', botId, signal, binding }); return gate ? gate.promise : snapshot; },
      steerQueued: async (botId, id, binding) => { calls.push({ action: 'steer', botId, id, binding }); if (mutationError) throw mutationError; },
      removeQueued: async (botId, id, binding) => { calls.push({ action: 'remove', botId, id, binding }); },
      resumeQueue: async (botId, binding) => { calls.push({ action: 'resume', botId, binding }); },
    } },
  };
  const exports = {};
  runInNewContext(source, { exports, require: name => modules[name], AbortController,
    setTimeout: callback => { const timer = { callback }; timers.push(timer); return timer; }, clearTimeout: timer => { if (timer) timer.cancelled = true; },
  });
  const render = () => { cursor = 0; effectCursor = 0; const result = exports.useMessageQueue('bot', scope, revision, offline, onError); for (const effect of pendingEffects.splice(0)) effect(); return result; };
  return { calls, errors, render,
    binding: value => exports.workspaceBinding(value),
    load: async () => { render(); for (const timer of timers.splice(0)) if (!timer.cancelled) timer.callback(); await settled(); return render(); },
    revision: value => { revision = value; render(); },
    scope: value => { scope = value; render(); },
    offline: value => { offline = value; render(); },
    gate: () => gate = deferred(),
    failMutation: error => mutationError = error,
    snapshot: next => snapshot = next,
    unmount: () => { for (const effect of effects) effect?.cleanup?.(); },
  };
}

test('text-only queued messages normalize omitted attachment arrays and equal refreshes retain identity', async () => {
  const view = queue({ snapshot: { messages: [{ id: 'q', text: 'Next instruction' }], paused: false } });
  const first = await view.load();
  assert.equal(first.snapshot.messages[0].attachments.length, 0);
  assert.equal(first.snapshot.messages[0].text, 'Next instruction');
  view.revision(12);
  const refreshed = await view.load();
  assert.equal(refreshed.snapshot, first.snapshot, 'no queue render when the server snapshot is unchanged');
  assert.deepEqual(JSON.parse(JSON.stringify(view.calls[0].binding)), { accountId: 'account-a', nodeId: 'mac-a' });
  view.unmount();
});

test('failed steer retains the queued item and reports the failure once', async () => {
  const view = queue({ snapshot: { messages: [{ id: 'q', text: 'Keep this instruction' }], paused: false } });
  const first = await view.load();
  view.failMutation(new Error('Harness rejected the steer'));
  const operation = first.steer('q');
  first.steer('q');
  await operation;
  const result = view.render();
  assert.equal(result.snapshot, first.snapshot);
  assert.equal(result.snapshot.messages[0].text, 'Keep this instruction');
  assert.equal(result.pending, '');
  assert.equal(result.error, 'Harness rejected the steer');
  assert.deepEqual(view.errors, ['Harness rejected the steer']);
  assert.equal(view.calls.filter(call => call.action === 'steer').length, 1);
  const refreshed = await view.load();
  assert.equal(refreshed.snapshot, first.snapshot);
  assert.equal(refreshed.error, 'Harness rejected the steer', 'authoritative refresh keeps the failure visible');
  view.unmount();
});

test('an ambiguous steer refreshes quarantine and paused queue without silently retrying', async () => {
  const view = queue({ snapshot: { messages: [{ id: 'uncertain', text: 'Accepted maybe' }, { id: 'next', text: 'Wait for resume' }], paused: false } });
  const first = await view.load();
  view.failMutation(new Error('Steer acceptance is uncertain. Queue paused.'));
  view.snapshot({ messages: [{ id: 'next', text: 'Wait for resume' }], paused: true });
  await first.steer('uncertain');
  const refreshed = await view.load();
  assert.deepEqual(Array.from(refreshed.snapshot.messages, message => message.id), ['next']);
  assert.equal(refreshed.snapshot.paused, true);
  assert.equal(refreshed.error, 'Steer acceptance is uncertain. Queue paused.');
  assert.equal(view.calls.filter(call => call.action === 'steer').length, 1);
  view.unmount();
});

test('a queue response for the previous account or node cannot populate the current composer', async () => {
  const view = queue();
  const pending = view.gate();
  await view.load();
  const oldRequest = view.calls[0];
  view.scope('account-b:mac-b');
  assert.equal(oldRequest.signal.aborted, true);
  pending.resolve({ messages: [{ id: 'private-a', text: 'Account A private message' }], paused: false });
  await settled();
  assert.equal(view.render().snapshot.messages.length, 0);
  view.unmount();
});

test('offline queue controls preserve instructions without issuing a request and reconnect refreshes', async () => {
  const view = queue({ snapshot: { messages: [{ id: 'q', text: 'Keep me' }], paused: true } });
  await view.load();
  view.offline(true);
  const offline = view.render();
  await offline.remove('q');
  await offline.resume();
  assert.equal(view.calls.length, 1);
  assert.equal(offline.snapshot.messages[0].id, 'q');
  view.offline(false);
  await view.load();
  assert.equal(view.calls.length, 2);
  view.unmount();
});
