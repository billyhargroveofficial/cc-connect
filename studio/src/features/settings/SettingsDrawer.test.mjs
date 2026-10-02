import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../../lib/motion-stub.mjs';

const tabTitle = node => Array.isArray(node.props.children) ? node.props.children[0] : node.props.children;

// Render the real settings tab controller and ModalShell. Pane workflows stay
// as component boundaries so this regression only exercises opening Settings.
function sharedSettings() {
  let cursor = 0;
  const values = [], calls = [];
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const element = (type, props, key) => ({ type, props, key });
  const modules = {
    react: {
      useState: state, useRef: initial => state(() => ({ current: initial }))[0],
      useId: () => state('shared-settings-test')[0], useEffect() {},
    },
    'react-dom': { createPortal: children => children },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    'react-markdown': { default: 'ReactMarkdown' },
    'remark-gfm': { default: 'remarkGfm' },
    '../../lib/api': { api: {} },
    '../../lib/motion': motionTestModule(),
    '../../lib/events': { isWorking: () => false, telegramLabel: () => 'Connected' },
    '../../lib/avatars': { avatarStyles: [] },
    '../../components/Avatar': { default: 'Avatar' },
    './settings.css': {},
  };
  function load(filename) {
    const source = ts.transpileModule(readFileSync(new URL(filename, import.meta.url), 'utf8'), {
      compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
    }).outputText;
    const exports = {};
    runInNewContext(source, {
      exports, document: { body: {} },
      require: name => {
        assert.ok(name in modules, `Unexpected settings import: ${name}`);
        return modules[name];
      },
    }, { filename });
    return exports;
  }
  modules['./shared'] = load('./shared.tsx');
  const { SettingsDrawer } = load('./SettingsDrawer.tsx');
  const props = {
    bot: null, bots: [{ id: 'bot-1', name: 'Researcher' }], capabilities: null, global: true,
    onClose: () => calls.push('close'), onBotChange() {}, onArchive() {},
  };
  function all(match) {
    cursor = 0;
    const matches = [];
    function visit(node) {
      if (!node || typeof node !== 'object') return;
      if (match(node)) matches.push(node);
      if (typeof node.type === 'function' && ['ModalShell', 'SettingsContent', 'SettingsTabPanel'].includes(node.type.name)) {
        visit(node.type(node.props));
        return;
      }
      const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children];
      for (const child of children.flat(Infinity)) visit(child);
    }
    visit(SettingsDrawer(props));
    return matches;
  }
  const find = match => all(match)[0];
  return {
    props, calls, all, find,
    tab: title => find(node => node.props?.role === 'tab' && tabTitle(node) === title),
    pane: () => find(node => typeof node.type === 'function' && node.type.name.endsWith('Pane')),
  };
}

test('shared Settings opens a modal instead of a side drawer and keeps shared instructions, skills and maintenance', () => {
  const view = sharedSettings();
  const dialog = view.find(node => node.props?.role === 'dialog');
  assert.ok(dialog, 'Shared settings must use the modal shell');
  assert.equal(dialog.props['aria-modal'], 'true');
  assert.equal(dialog.props['aria-label'], 'Shared settings');
  assert.equal(view.all(node => typeof node.props?.className === 'string' && node.props.className.includes('--drawer')).length, 0,
    'Desktop Shared settings must not use the slide-out drawer presentation');
  assert.deepEqual(view.all(node => node.props?.role === 'tab').map(tabTitle), [
    'Instructions', 'Skills', 'Maintenance',
  ]);
  assert.equal(view.pane().type.name, 'InstructionsPane');
  assert.equal(view.pane().props.id, undefined, 'Instructions use shared scope');
  view.tab('Skills').props.onClick();
  assert.equal(view.pane().type.name, 'SkillsPane');
  assert.equal(view.pane().props.bot, null, 'Skills use shared scope');
  view.tab('Maintenance').props.onClick();
  assert.equal(view.pane().type.name, 'MaintenancePane');
  assert.equal(view.pane().props.bots, view.props.bots);

  const overlay = view.find(node => node.type === 'div' && node.props?.className?.split(' ').includes('cb-settings-overlay'));
  const target = {};
  overlay.props.onClick({ target: {}, currentTarget: target });
  assert.deepEqual(view.calls, [], 'Clicks inside the dialog must not dismiss settings');
  overlay.props.onClick({ target, currentTarget: target });
  view.find(node => node.props?.['aria-label'] === 'Close Shared settings').props.onClick();
  assert.deepEqual(view.calls, ['close', 'close']);
});
