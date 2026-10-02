import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../../lib/motion-stub.mjs';

const settled = () => new Promise(resolve => setImmediate(resolve));
const initialBot = {
  id: 'researcher', name: 'Researcher', role: 'Find original sources', avatar: 'mint', chief: false,
  backend: 'codex', model: 'gpt-6-sol', effort: 'max', status: 'idle',
  workDir: '/bots/researcher', telegram: { enabled: false },
};

function paneHarness(kind = 'instructions', { global = false, saveFailure = false } = {}) {
  let cursor = 0, effectCursor = 0;
  const values = [], effects = [], pending = [], calls = [], updated = [];
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const element = (type, props, key) => ({ type, props, key });
  const props = {
    id: global ? undefined : initialBot.id, bot: global ? null : { ...initialBot }, capabilities: null,
    onBotChange: bot => { updated.push(bot); props.bot = bot; }, onArchive() {},
  };
  const api = {
    async instructions(id) { calls.push(['load', id]); return { content: '# Working rules', path: '/AGENTS.md' }; },
    async updateBot(id, fields) {
      calls.push(['bot', id, fields]);
      return { ...props.bot, ...fields };
    },
    async saveInstructions(id, content) {
      calls.push(['instructions', id, content]);
      if (saveFailure) { saveFailure = false; throw new Error('Could not save AGENTS.md'); }
      return { content, path: '/AGENTS.md' };
    },
  };
  const modules = {
    react: {
      useState: state, useRef: initial => state(() => ({ current: initial }))[0],
      useId: () => state('purpose-test')[0],
      createElement: (type, props, ...children) => element(type, { ...props, children: children.length > 1 ? children : children[0] }, props?.key),
      useEffect(callback, dependencies) {
        const index = effectCursor++, previous = effects[index];
        if (!previous || dependencies.some((value, offset) => !Object.is(value, previous.dependencies?.[offset]))) {
          pending.push(() => { previous?.cleanup?.(); effects[index] = { dependencies, cleanup: callback() }; });
        }
      },
    },
    'react-dom': { createPortal: children => children },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    'react-markdown': { default: 'ReactMarkdown' },
    'remark-gfm': { default: 'remarkGfm' },
    '../../lib/api': { api },
    '../../lib/events': { isWorking: () => false, telegramLabel: () => 'Connected' },
    '../../lib/motion': motionTestModule(),
    '../../lib/avatars': { avatarStyles: [] },
    '../../components/Avatar': { default: 'Avatar' },
    './settings.css': {},
  };
  function load(filename, extra = '') {
    const source = ts.transpileModule(readFileSync(new URL(filename, import.meta.url), 'utf8'), {
      compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
    }).outputText;
    const exports = {};
    runInNewContext(source + extra, { exports, require: name => {
      assert.ok(name in modules, `Unexpected settings import: ${name}`); return modules[name];
    } }, { filename });
    return exports;
  }
  const shared = load('./shared.tsx');
  modules['./shared'] = shared;
  // Exercise the real private pane workflows without mounting the rest of
  // the workspace or adding test-only exports to the product API.
  const panes = load('./SettingsDrawer.tsx', '\nexports.testProfilePane = ProfilePane; exports.testInstructionsPane = InstructionsPane;');
  const component = kind === 'profile' ? panes.testProfilePane : panes.testInstructionsPane;
  function all(match) {
    cursor = 0; effectCursor = 0;
    const found = [];
    function visit(node) {
      if (!node || typeof node !== 'object') return;
      if (match(node)) found.push(node);
      if (typeof node.type === 'function' && ['BotFields', 'RuntimeFields'].includes(node.type.name)) {
        visit(node.type(node.props)); return;
      }
      const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children];
      children.flat(Infinity).forEach(visit);
    }
    visit(component(props));
    for (const effect of pending.splice(0)) effect();
    return found;
  }
  const find = match => all(match)[0];
  return {
    props, calls, updated, all, find, payload: shared.botPayload,
    role: () => find(node => node.type === 'textarea'),
    editor: () => find(node => typeof node.type === 'function' && node.type.name === 'MarkdownEditor'),
    saveButton: () => find(node => typeof node.type === 'function' && node.type.name === 'SaveButton'),
    submit: () => find(node => node.type === 'form').props.onSubmit({ preventDefault() {} }),
  };
}

test('Profile has no Purpose editor and saving identity cannot overwrite the separately edited purpose', async () => {
  const view = paneHarness('profile');
  assert.equal(view.role(), undefined, 'Purpose belongs only to Instructions');
  const name = view.find(node => node.type === 'input' && node.props.required && node.props.maxLength === 80);
  name.props.onChange({ target: { value: 'Sources bot' } });
  await view.submit();
  const save = view.calls.find(call => call[0] === 'bot');
  assert.equal(save[1], initialBot.id);
  assert.equal(save[2].name, 'Sources bot');
  assert.equal(Object.hasOwn(save[2], 'role'), false, 'Profile PATCH does not include its stale role snapshot');
  assert.equal(view.updated[0].role, initialBot.role);
});

test('Instructions saves a short Purpose through the bot API without rewriting unchanged AGENTS.md', async () => {
  const view = paneHarness();
  view.all(() => false); await settled();
  assert.equal(view.all(node => node.type === 'textarea').length, 1, 'exactly one short Purpose editor');
  assert.equal(view.saveButton().props.disabled, true);
  view.role().props.onChange({ target: { value: '  Verify facts  ' } });
  assert.equal(view.saveButton().props.disabled, false);
  await view.submit();
  const save = view.calls.find(call => call[0] === 'bot');
  assert.equal(save[1], initialBot.id);
  assert.deepEqual(Object.keys(save[2]), ['role']);
  assert.equal(save[2].role, 'Verify facts');
  assert.equal(view.calls.some(call => call[0] === 'instructions'), false);
  assert.equal(view.updated[0].role, 'Verify facts');
  assert.equal(view.saveButton().props.disabled, true);
});

test('editing AGENTS.md only writes instruction content and preserves the bot identity and runtime', async () => {
  const view = paneHarness();
  view.all(() => false); await settled();
  view.editor().props.onChange('# Cite sources');
  await view.submit();
  assert.equal(view.calls.some(call => call[0] === 'bot'), false);
  assert.deepEqual(view.calls.find(call => call[0] === 'instructions'), ['instructions', initialBot.id, '# Cite sources']);
  assert.equal(view.saveButton().props.disabled, true);
});

test('a partial Purpose and AGENTS.md save retries only the failed document without losing either draft', async () => {
  const view = paneHarness('instructions', { saveFailure: true });
  view.all(() => false); await settled();
  view.role().props.onChange({ target: { value: 'Verify facts' } });
  view.editor().props.onChange('# New working rules');
  await view.submit();
  assert.equal(view.role().props.value, 'Verify facts');
  assert.equal(view.editor().props.content, '# New working rules');
  assert.equal(view.saveButton().props.disabled, false, 'the failed document remains dirty');
  await view.submit();
  assert.equal(view.calls.filter(call => call[0] === 'bot').length, 1, 'the already-saved purpose is not resent');
  assert.equal(view.calls.filter(call => call[0] === 'instructions').length, 2);
  assert.equal(view.saveButton().props.disabled, true);
});

test('incoming Purpose updates refresh untouched input while preserving a locally edited Purpose', async () => {
  const view = paneHarness();
  view.all(() => false); await settled();
  view.props.bot = { ...view.props.bot, role: 'Remote purpose' };
  view.all(() => false);
  assert.equal(view.role().props.value, 'Remote purpose');
  view.role().props.onChange({ target: { value: 'My unsaved purpose' } });
  view.props.bot = { ...view.props.bot, role: 'New remote purpose' };
  view.all(() => false);
  assert.equal(view.role().props.value, 'My unsaved purpose');
  assert.equal(view.saveButton().props.disabled, false);
});

test('shared Instructions retain a document-only editor and save through shared scope', async () => {
  const view = paneHarness('instructions', { global: true });
  view.all(() => false); await settled();
  assert.equal(view.role(), undefined, 'shared settings have no bot Purpose or bot runtime controls');
  view.editor().props.onChange('# Shared rules');
  await view.submit();
  assert.equal(view.calls.some(call => call[0] === 'bot'), false);
  assert.deepEqual(view.calls.find(call => call[0] === 'instructions'), ['instructions', undefined, '# Shared rules']);
});

test('new bot payloads keep their initial Purpose while profile payloads explicitly exclude it', () => {
  const view = paneHarness('profile');
  const draft = {
    ...initialBot, telegramEnabled: false, telegramTokenEnv: '', telegramAllowedUserIds: '',
  };
  assert.equal(view.payload(draft).role, initialBot.role);
  assert.equal(Object.hasOwn(view.payload(draft, { includeRole: false }), 'role'), false);
});
