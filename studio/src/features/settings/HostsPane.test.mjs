import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../../lib/motion-stub.mjs';

const source = ts.transpileModule(readFileSync(new URL('./SettingsDrawer.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const settled = () => new Promise(resolve => setImmediate(resolve));
const local = { id: 'local', name: 'Server', local: true, online: true, os: 'linux', arch: 'amd64' };
const mac = { id: 'node-mac', name: 'MacBook', local: false, online: false, hostname: 'Billy-Mac', os: 'darwin', arch: 'arm64' };

function hosts(overrides = {}) {
  let cursor = 0, effectCursor = 0, present = true, confirmationPresent = true;
  const rendering = [];
  const values = [], effects = [], pendingEffects = [], calls = [], copies = [];
  const timers = new Map();
  let nextTimer = 1;
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const element = (type, props) => {
    if (typeof type !== 'function') return { type, props };
    rendering.push(type.name);
    try { return type(props); } finally { rendering.pop(); }
  };
  const modules = {
    react: {
      useState: state, useRef: initial => state(() => ({ current: initial }))[0], useId: () => state('hosts-test')[0],
      useEffect(callback, dependencies) {
        const index = effectCursor++, previous = effects[index];
        if (!previous || !dependencies || dependencies.some((value, offset) => !Object.is(value, previous.dependencies?.[offset]))) {
          pendingEffects.push(() => { previous?.cleanup?.(); effects[index] = { dependencies, cleanup: callback() }; });
        }
      },
    },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../../lib/api': { api: {} },
    '../../lib/events': {},
    '../../lib/motion': { ...motionTestModule(), useIsPresent: () => rendering.at(-1) === 'HostRemovalConfirmation' ? confirmationPresent : present },
    './shared': {
      SaveButton: props => element('button', { ...props, type: 'submit', disabled: props.busy || props.disabled }),
      Notice: 'Notice', errorMessage: error => error.message,
    },
  };
  const exports = {};
  const timer = callback => { const id = nextTimer++; timers.set(id, callback); return id; };
  runInNewContext(source, {
    exports, navigator: { clipboard: { writeText: async value => copies.push(value) } },
    window: { location: { origin: 'http://192.168.1.15:5173' }, setInterval: timer, clearInterval: id => timers.delete(id), setTimeout: timer, clearTimeout: id => timers.delete(id) },
    require: name => { assert.ok(name in modules, `Unexpected HostsPane import: ${name}`); return modules[name]; },
  }, { filename: 'SettingsDrawer.tsx' });
  const enrollment = { nodeId: mac.id, code: 'SECRET-PAIR-CODE', expiresAt: new Date(Date.now() + 600_000).toISOString(), serverUrl: 'http://192.168.1.15:9830' };
  const props = {
    nodes: [local, mac], activeNode: local,
    onCreateNodeEnrollment: async name => { calls.push(['enroll', name]); return enrollment; },
    onRemoveNode: async id => calls.push(['remove', id]),
    onRefreshNodes: async () => calls.push(['refresh']),
    onSelectNode: id => calls.push(['select', id]),
    nodeBinaryURL: (platform, arch) => `/binary/${platform}/${arch}`,
    ...overrides,
  };
  const render = () => { cursor = 0; effectCursor = 0; const tree = exports.HostsPane(props); for (const effect of pendingEffects.splice(0)) effect(); return tree; };
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
  const text = node => node.props?.children;
  const button = label => find(node => node.type === 'button' && text(node) === label);
  return {
    props, calls, copies, enrollment, all, find, render, button,
    pairCommand: exports.nodePairCommand,
    start: () => find(node => node.type === 'button' && Array.isArray(text(node)) && text(node).includes('Add host')).props.onClick(),
    name: value => find(node => node.type === 'input' && node.props.placeholder === 'My MacBook').props.onChange({ target: { value } }),
    continue: () => find(node => node.type === 'form').props.onSubmit({ preventDefault() {} }),
    code: () => find(node => node.props?.['aria-label'] === 'Pairing code'),
    exit: () => { present = false; render(); },
    exitConfirmation: () => { confirmationPresent = false; render(); },
    unmount: () => { for (const effect of effects) effect?.cleanup?.(); },
  };
}

test('Hosts lists status and platform while the built-in server cannot be removed', () => {
  const view = hosts();
  assert.equal(view.all(node => node.props?.className === 'cb-host-row').length, 2);
  assert.equal(view.find(node => node.props?.['aria-label'] === 'Remove Server'), undefined);
  assert.ok(view.find(node => node.props?.['aria-label'] === 'Remove MacBook'));
  assert.equal(view.all(node => node.props?.className?.includes('cb-host-online')).length, 2);
  view.unmount();
});

test('host pairing provides both Mac binaries and an interactive command without exposing the pairing secret in shell history', async () => {
  const view = hosts();
  view.start(); view.name('  My Mac  '); await view.continue();
  assert.deepEqual(view.calls[0], ['enroll', 'My Mac']);
  assert.equal(view.code().props.value, view.enrollment.code);
  assert.deepEqual(view.all(node => node.type === 'a').map(node => node.props.href), ['/binary/darwin/arm64', '/binary/darwin/amd64']);
  const command = view.find(node => node.props?.className === 'cb-host-copy-block').props.children[0].props.children;
  assert.equal(command, "./connect-bots-node pair --server 'http://192.168.1.15:9830' --allow-insecure");
  assert.ok(!command.includes(view.enrollment.code));
  await view.find(node => node.props?.['aria-label'] === 'Copy pairing command').props.onClick();
  await view.find(node => node.props?.['aria-label'] === 'Copy pairing code').props.onClick();
  assert.deepEqual(view.copies, [command, view.enrollment.code]);
  view.props.nodes = [local, { ...mac, online: true }];
  assert.equal(view.code(), undefined, 'a connected host no longer exposes its one-time code');
  view.button('Use this host').props.onClick();
  assert.ok(view.calls.some(call => call[0] === 'select' && call[1] === mac.id));
  assert.equal(view.code(), undefined);
  view.unmount();
});

test('expired pairing codes disappear and can be renewed', async () => {
  const view = hosts({ onCreateNodeEnrollment: async () => ({ nodeId: 'new', code: 'EXPIRED', expiresAt: '2000-01-01T00:00:00Z' }) });
  view.start(); view.name('Mac'); await view.continue();
  assert.equal(view.code(), undefined);
  assert.ok(view.button('Try again'));
  view.button('Try again').props.onClick();
  assert.ok(view.find(node => node.type === 'input' && node.props.placeholder === 'My MacBook'));
  view.unmount();
});

test('removing a remote host requires explicit confirmation and reports a failed disconnect', async () => {
  const view = hosts({ onRemoveNode: async id => { view.calls.push(['remove', id]); throw new Error('Host could not be removed'); } });
  view.find(node => node.props?.['aria-label'] === 'Remove MacBook').props.onClick();
  assert.deepEqual(view.calls, []);
  const confirm = view.find(node => node.type === 'button' && Array.isArray(node.props.children) && node.props.children.includes('Remove host'));
  assert.ok(confirm);
  await confirm.props.onClick();
  await settled();
  assert.deepEqual(view.calls, [['remove', mac.id]]);
  assert.equal(view.find(node => node.type === 'Notice').props.error, 'Host could not be removed');
  assert.ok(view.find(node => node.props?.['aria-label'] === 'Remove MacBook'));
  view.unmount();
});

test('an exiting host removal confirmation cannot disconnect the host while its last props are retained for animation', async () => {
  const view = hosts();
  view.find(node => node.props?.['aria-label'] === 'Remove MacBook').props.onClick();
  const confirmation = () => view.find(node => node.props?.className === 'cb-host-remove-confirm');
  assert.equal(confirmation().props.inert, false);
  assert.equal(view.button('Cancel').props.disabled, false);
  view.exitConfirmation();
  assert.equal(confirmation().props.inert, true);
  assert.equal(confirmation().props['aria-hidden'], true);
  assert.equal(view.button('Cancel').props.disabled, true);
  const remove = view.find(node => node.type === 'button' && Array.isArray(node.props.children) && node.props.children.includes('Remove host'));
  assert.equal(remove.props.disabled, true);
  await remove.props.onClick();
  assert.deepEqual(view.calls, [], 'an already queued click cannot submit an exiting confirmation');
  view.unmount();
});

test('a pairing enrollment that resolves during exit cannot display the old account code', async () => {
  let resolve;
  const view = hosts({ onCreateNodeEnrollment: () => new Promise(done => { resolve = done; }) });
  view.start(); view.name('Mac'); const request = view.continue(); view.exit();
  resolve(view.enrollment); await request;
  assert.equal(view.code(), undefined);
  view.unmount();
});

test('HTTPS pairing omits the insecure flag and safely quotes server URLs', () => {
  const view = hosts();
  assert.equal(view.pairCommand({ serverUrl: "https://host.test/a'b", code: 'SECRET' }, ''), "./connect-bots-node pair --server 'https://host.test/a'\\''b'");
  view.unmount();
});
