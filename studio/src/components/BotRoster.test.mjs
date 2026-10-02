import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';

const source = ts.transpileModule(readFileSync(new URL('./BotRoster.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function renderRoster(bots, onCreate) {
  const element = (type, props) => ({ type, props });
  const modules = {
    'react/jsx-runtime': { jsx: element, jsxs: element },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../lib/events': {
      isWorking: () => false,
      messagePreview: () => '',
      statusLabel: () => 'Ready',
      telegramTitle: () => 'Connected',
    },
    './Avatar': { default: 'Avatar' },
    './ThemePicker': { default: 'ThemePicker' },
  };
  const exports = {};
  runInNewContext(source, {
    exports,
    require: name => {
      assert.ok(name in modules, `Unexpected BotRoster import: ${name}`);
      return modules[name];
    },
  }, { filename: 'BotRoster.tsx' });
  return exports.default({
    bots, events: {}, selectedId: '', onCreate,
    onSelect() {}, onSettings() {}, onTheme() {}, onLogout() {},
    theme: 'system', connection: 'connected',
  });
}

function allIn(root, match) {
  const matches = [];
  function visit(node) {
    if (!node || typeof node !== 'object') return;
    if (match(node)) matches.push(node);
    const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children];
    for (const child of children.flat(Infinity)) visit(child);
  }
  visit(root);
  return matches;
}

test('Create bot stays accessible in the shared footer without reserving a header row', () => {
  for (const bots of [[], [{ id: 'bot-1', name: 'Researcher', status: 'idle', createdAt: '2026-10-02', role: 'Research' }]]) {
    let createCalls = 0;
    const roster = renderRoster(bots, () => { createCalls++; });
    const controls = allIn(roster, node => node.type === 'button' && node.props?.['aria-label'] === 'Create bot');
    assert.equal(controls.length, 1, 'creation remains available in both empty and populated rosters');
    assert.equal(allIn(roster, node => node.type === 'header').length, 0, 'the bot list starts without a separate toolbar row');
    const footerActions = allIn(roster, node => node.props?.className === 'roster-footer-actions')[0];
    assert.ok(footerActions);
    assert.equal(allIn(footerActions, node => node === controls[0]).length, 1, 'Create bot shares the existing footer controls');
    assert.notEqual(controls[0].props.disabled, true);
    controls[0].props.onClick();
    assert.equal(createCalls, 1, 'the relocated control still opens bot creation');
  }
});
