import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';

const source = ts.transpileModule(readFileSync(new URL('./NewBotDialog.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const settled = () => new Promise(resolve => setImmediate(resolve));
const local = { id: 'local', name: 'Server', local: true, online: true };
const mac = { id: 'mac', name: 'MacBook', local: false, online: true };
const capabilities = (backend, id) => ({ backends: { codex: { available: backend === 'codex' }, pi: { available: backend === 'pi' } }, models: [{ backend, id, efforts: ['medium', 'max'] }] });
function deferred() {
  let resolve, reject;
  const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

function dialog(overrides = {}) {
  let cursor = 0, effectCursor = 0;
  const values = [], effects = [], pending = [], loads = [], creates = [], changed = [], created = [], closes = [];
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const element = (type, props) => typeof type === 'function' ? type(props) : ({ type, props });
  const modules = {
    react: {
      useState: state, useRef: initial => state(() => ({ current: initial }))[0],
      useEffect(callback, dependencies) {
        const index = effectCursor++, previous = effects[index];
        if (!previous || dependencies.some((value, offset) => !Object.is(value, previous.dependencies?.[offset]))) {
          pending.push(() => { previous?.cleanup?.(); effects[index] = { dependencies, cleanup: callback() }; });
        }
      },
    },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../../lib/api': { api: { createBot() { throw new Error('Unscoped bot creation must not be used'); } } },
    './shared': {
      BotFields: props => element('BotFields', { ...props, children: props.beforeModel }),
      botPayload: draft => ({ ...draft }),
      draftFromBot: (_, caps) => {
        const model = caps?.models[0];
        return { name: '', role: '', backend: model?.backend || 'codex', model: model?.id || '', effort: model ? 'max' : '' };
      },
      errorMessage: error => error.message, ModalShell: 'ModalShell', Notice: 'Notice',
      SaveButton: props => element('button', { ...props, type: 'submit', disabled: props.busy || props.disabled }),
    },
  };
  const exports = {};
  runInNewContext(source, { exports, AbortController, require: name => {
    assert.ok(name in modules, `Unexpected NewBotDialog import: ${name}`); return modules[name];
  } }, { filename: 'NewBotDialog.tsx' });
  const props = {
    capabilities: capabilities('codex', 'server-model'), nodes: [local, mac], activeNode: local,
    onClose: () => closes.push(true), onCreated: bot => created.push(bot),
    onNodeChange: id => { changed.push(id); props.activeNode = props.nodes.find(node => node.id === id); },
    loadCapabilities: (nodeId, signal) => { const request = { nodeId, signal, ...deferred() }; loads.push(request); return request.promise; },
    createBot: (fields, nodeId) => { const request = { fields, nodeId, ...deferred() }; creates.push(request); return request.promise; },
    ...overrides,
  };
  const render = () => { cursor = 0; effectCursor = 0; const tree = exports.NewBotDialog(props); for (const effect of pending.splice(0)) effect(); return tree; };
  function all(match) {
    const found = [];
    function visit(node) {
      if (!node || typeof node !== 'object') return;
      if (match(node)) found.push(node);
      const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children];
      children.flat(Infinity).forEach(visit);
    }
    visit(render()); return found;
  }
  const find = match => all(match)[0];
  const fields = () => find(node => node.type === 'BotFields');
  return {
    props, loads, creates, changed, created, closes, render, all, find, fields,
    setName: name => { const control = fields(); control.props.onChange({ ...control.props.value, name }); },
    choose: id => find(node => node.type === 'select').props.onChange({ target: { value: id } }),
    submit: () => find(node => node.type === 'form').props.onSubmit({ preventDefault() {} }),
    disabled: () => find(node => node.type === 'button' && node.props.type === 'submit').props.disabled,
    unmount: () => { for (const effect of effects) effect?.cleanup?.(); },
  };
}

test('New bot loads the chosen host models and explicitly creates the bot on that host', async () => {
  const view = dialog(); view.render();
  assert.equal(view.loads[0].nodeId, local.id);
  view.loads[0].resolve(capabilities('codex', 'server-model')); await settled();
  view.setName('Personal bot'); view.choose(mac.id); view.render();
  assert.deepEqual(view.changed, [mac.id]);
  assert.equal(view.loads.at(-1).nodeId, mac.id);
  assert.equal(view.disabled(), true, 'creation waits for the target host models');
  view.loads.at(-1).resolve(capabilities('pi', 'mac-model')); await settled();
  assert.equal(view.fields().props.value.name, 'Personal bot', 'changing host retains the bot identity');
  assert.equal(view.fields().props.value.model, 'mac-model');
  const request = view.submit();
  assert.equal(view.creates[0].nodeId, mac.id);
  assert.equal(view.creates[0].fields.model, 'mac-model');
  view.creates[0].resolve({ id: 'created-mac-bot' }); await request;
  assert.equal(view.created[0].id, 'created-mac-bot');
  assert.deepEqual(view.closes, [true]);
  view.unmount();
});

test('a late capabilities response from the previous host cannot overwrite the new host models', async () => {
  const view = dialog(); view.render();
  const old = view.loads[0]; view.choose(mac.id); view.render();
  const current = view.loads.at(-1);
  assert.equal(old.signal.aborted, true);
  current.resolve(capabilities('pi', 'mac-model')); await settled();
  old.resolve(capabilities('codex', 'late-server-model')); await settled();
  assert.equal(view.fields().props.value.model, 'mac-model');
  assert.equal(view.fields().props.capabilities.models[0].id, 'mac-model');
  view.unmount();
});

test('an offline target keeps the form draft and blocks creation until its models are available again', async () => {
  const offlineMac = { ...mac, online: false };
  const view = dialog({ nodes: [local, offlineMac], activeNode: offlineMac }); view.render();
  view.setName('Keep my draft');
  assert.equal(view.loads.length, 0, 'an offline host is not queried');
  assert.equal(view.disabled(), true);
  await view.submit(); assert.equal(view.creates.length, 0);
  view.props.nodes = [local, mac]; view.props.activeNode = mac; view.render();
  assert.equal(view.loads[0].nodeId, mac.id);
  view.loads[0].resolve(capabilities('codex', 'mac-model')); await settled();
  assert.equal(view.fields().props.value.name, 'Keep my draft');
  assert.equal(view.disabled(), false);
  view.unmount();
});

test('a missing target host blocks model loading and creation until an available host is selected', async () => {
  const view = dialog({ nodes: [mac], activeNode: undefined }); view.render();
  view.setName('Keep my draft');
  assert.equal(view.loads.length, 0, 'an unknown local host is not queried');
  assert.equal(view.disabled(), true);
  await view.submit(); assert.equal(view.creates.length, 0);
  view.choose(mac.id); view.render();
  assert.equal(view.loads[0].nodeId, mac.id);
  view.loads[0].resolve(capabilities('codex', 'mac-model')); await settled();
  assert.equal(view.fields().props.value.name, 'Keep my draft');
  assert.equal(view.disabled(), false);
  const request = view.submit();
  assert.equal(view.creates[0].nodeId, mac.id);
  view.creates[0].resolve({ id: 'mac-bot' }); await request;
  view.unmount();
});

test('removing the chosen host invalidates stale activeNode metadata and late model responses', async () => {
  const view = dialog({ nodes: [local, mac], activeNode: mac }); view.render();
  const request = view.loads[0];
  view.setName('Retain my draft');
  view.props.nodes = [local]; view.render();
  assert.equal(request.signal.aborted, true, 'removal cancels the target host lookup even if activeNode is stale');
  request.resolve(capabilities('codex', 'removed-host-model')); await settled();
  assert.equal(view.fields().props.capabilities, null);
  assert.equal(view.disabled(), true);
  await view.submit(); assert.equal(view.creates.length, 0);
  view.choose(local.id); view.render();
  view.loads.at(-1).resolve(capabilities('codex', 'server-model')); await settled();
  assert.equal(view.fields().props.value.name, 'Retain my draft');
  assert.equal(view.fields().props.value.model, 'server-model');
  assert.equal(view.disabled(), false);
  view.unmount();
});

test('a creation response after account dialog unmount cannot select a bot in the next workspace', async () => {
  const view = dialog(); view.render();
  view.loads[0].resolve(capabilities('codex', 'server-model')); await settled(); view.setName('Bot');
  const request = view.submit(); view.unmount();
  view.creates[0].resolve({ id: 'previous-account-bot' }); await request;
  assert.deepEqual(view.created, []); assert.deepEqual(view.closes, []);
});
