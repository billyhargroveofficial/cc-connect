import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';

const source = ts.transpileModule(readFileSync(new URL('./SettingsDrawer.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function panel() {
  let cursor = 0;
  const values = [], calls = [];
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const element = (type, props, key) => ({ type, props, key });
  const modules = {
    react: { useState: state, useId: () => state('embedded-settings-test')[0], useEffect() {} },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../../lib/api': { api: {} },
    '../../lib/events': { isWorking: () => false, telegramLabel: () => 'Connected' },
    './shared': {
      BotFields: 'BotFields', MarkdownEditor: 'MarkdownEditor', ModalShell: 'ModalShell',
      Notice: 'Notice', RuntimeFields: 'RuntimeFields', SaveButton: 'SaveButton',
      botPayload: value => value, draftFromBot: bot => bot, errorMessage: error => error.message,
    },
  };
  const exports = {};
  runInNewContext(source, {
    exports,
    require: name => {
      assert.ok(name in modules, `Unexpected settings import: ${name}`);
      return modules[name];
    },
  }, { filename: 'SettingsDrawer.tsx' });
  const props = {
    bot: { id: 'bot-1', name: 'Researcher' }, capabilities: null,
    onClose: () => calls.push('close'),
    onBotChange: bot => calls.push(['update', bot]),
    onArchive: id => calls.push(['archive', id]),
  };
  const render = () => { cursor = 0; return exports.BotSettingsPanel(props); };
  function all(match) {
    const matches = [];
    function visit(node) {
      if (!node || typeof node !== 'object') return;
      if (match(node)) matches.push(node);
      // Expand the real shared tab controller while leaving the independent
      // pane workflows as component boundaries, as React would render them.
      if (typeof node.type === 'function' && node.type.name === 'SettingsContent') {
        visit(node.type(node.props));
        return;
      }
      const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children];
      for (const child of children.flat(Infinity)) visit(child);
    }
    visit(render());
    return matches;
  }
  const find = match => all(match)[0];
  return {
    props, calls, find, all,
    reconcileBotDraft: exports.reconcileBotDraft,
    tab: title => find(node => node.props?.role === 'tab' && node.props.children === title),
    pane: () => find(node => typeof node.type === 'function' && node.type.name.endsWith('Pane')),
  };
}

test('embedded bot settings expose profile, instructions and skills inside the island without a modal shell', () => {
  const view = panel();
  assert.equal(view.find(node => node.type === 'ModalShell'), undefined);
  assert.equal(view.find(node => node.props?.role === 'dialog'), undefined, 'the island owns modal behavior');
  assert.deepEqual(view.all(node => node.props?.role === 'tab').map(node => node.props.children), [
    'Profile', 'Instructions', 'Skills',
  ]);
  assert.equal(view.tab('Profile').props['aria-selected'], true);
  assert.equal(view.pane().type.name, 'ProfilePane');

  for (const [title, component] of [['Instructions', 'InstructionsPane'], ['Skills', 'SkillsPane']]) {
    view.tab(title).props.onClick();
    const tab = view.tab(title);
    const content = view.find(node => node.props?.role === 'tabpanel');
    assert.equal(tab.props['aria-selected'], true);
    assert.equal(tab.props['aria-controls'], content.props.id);
    assert.equal(content.props['aria-labelledby'], tab.props.id);
    assert.equal(view.pane().type.name, component);
  }
  assert.deepEqual(view.calls, [], 'switching tabs does not close settings');
});

test('incoming bot changes refresh untouched fields without overwriting local edits', () => {
  const view = panel();
  const previous = {
    name: 'Researcher', role: 'Find sources', avatar: 'mint', chief: false,
    backend: 'codex', model: 'gpt-6-sol', effort: 'max', telegramEnabled: false,
    telegramTokenEnv: '', telegramAllowedUserIds: '',
  };
  const current = { ...previous, role: 'My unsaved role' };
  const next = { ...previous, role: 'Role from another client', model: 'gpt-6-astra', effort: 'high' };
  const merged = view.reconcileBotDraft(current, previous, next);
  assert.equal(merged.role, 'My unsaved role');
  assert.equal(merged.model, 'gpt-6-astra');
  assert.equal(merged.effort, 'high');

  const localRuntime = { ...previous, model: 'gpt-6-astra', effort: 'ultra' };
  const remoteRuntime = { ...previous, backend: 'pi', model: 'deepseek-v4.1-flash', effort: 'high' };
  const runtimeMerged = view.reconcileBotDraft(localRuntime, previous, remoteRuntime);
  assert.equal(runtimeMerged.backend, 'codex');
  assert.equal(runtimeMerged.model, 'gpt-6-astra');
  assert.equal(runtimeMerged.effort, 'ultra');
});

test('embedded profile forwards changes and archiving while its close button collapses the island settings', () => {
  const view = panel();
  const profile = view.pane();
  assert.equal(profile.props.bot, view.props.bot);
  const updated = { ...view.props.bot, name: 'Updated researcher' };
  profile.props.onBotChange(updated);
  assert.deepEqual(view.calls, [['update', updated]]);
  profile.props.onArchive(view.props.bot.id);
  assert.deepEqual(view.calls, [['update', updated], ['archive', 'bot-1'], 'close']);
  view.find(node => node.props?.['aria-label'] === 'Close bot settings').props.onClick();
  assert.deepEqual(view.calls, [['update', updated], ['archive', 'bot-1'], 'close', 'close']);
});
