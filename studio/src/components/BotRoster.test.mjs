import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../lib/motion-stub.mjs';

const source = ts.transpileModule(readFileSync(new URL('./BotRoster.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function renderRoster(bots, onCreate, overrides = {}) {
  const element = (type, props) => typeof type === 'function' ? type(props) : ({ type, props });
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
    './SidebarResizeHandle': { default: 'SidebarResizeHandle' },
    './ThemePicker': { default: 'ThemePicker' },
    '../lib/motion': { ...motionTestModule(), useIsPresent: () => overrides.present !== false },
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
    ...overrides,
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

test('animated bot rows preserve native selection and hide inactive mobile navigation', () => {
  const bot = { id: 'bot-1', name: 'Researcher', status: 'idle', createdAt: '2026-10-02', role: 'Research' };
  const selections = [];
  const roster = renderRoster([bot], () => {}, {
    selectedId: bot.id,
    mobileHidden: true,
    onSelect: id => selections.push(id),
  });
  assert.equal(roster.type, 'aside');
  assert.equal(roster.props.inert, true, 'the hidden mobile roster cannot retain keyboard or pointer interaction');
  assert.equal(roster.props['aria-hidden'], true);
  const row = allIn(roster, node => node.type === 'button' && node.props?.['aria-current'] === 'page')[0];
  assert.equal(row.props.type, 'button');
  assert.equal(row.props.disabled, false);
  row.props.onClick();
  assert.deepEqual(selections, [bot.id]);
  const marker = allIn(row, node => node.props?.className === 'bot-row-selection')[0];
  assert.equal(marker.props['aria-hidden'], 'true', 'the moving selection marker stays decorative');
});

test('departing animated bot rows cannot be selected during their exit', () => {
  const bot = { id: 'bot-1', name: 'Researcher', status: 'idle', createdAt: '2026-10-02' };
  const roster = renderRoster([bot], () => {}, { present: false });
  const row = allIn(roster, node => node.type === 'button' && node.props?.className?.startsWith('bot-row '))[0];
  assert.equal(row.props.disabled, true);
  assert.equal(row.props['aria-hidden'], true);
});

test('roster layout measurements follow ordering and selection instead of streaming updates', () => {
  const bots = [
    { id: 'bot-1', name: 'Researcher', status: 'idle', chief: false, createdAt: '2026-10-01' },
    { id: 'bot-2', name: 'Builder', status: 'idle', chief: false, createdAt: '2026-10-02' },
  ];
  const dependencies = (team = bots, overrides = {}) => allIn(
    renderRoster(team, () => {}, { selectedId: 'bot-1', ...overrides }),
    node => node.type === 'button' && node.props?.className?.startsWith('bot-row '),
  ).map(row => row.props.layoutDependency);
  const original = dependencies();
  assert.equal(typeof original[0], 'string');
  assert.equal(original[0], original[1], 'all rows share the same layout trigger');
  assert.deepEqual(dependencies(bots.map(bot => ({ ...bot, status: 'working' })), {
    events: { 'bot-1': [{ type: 'native', seq: 2, time: '2026-10-02T10:00:00Z', data: { delta: 'Streaming response' } }] },
  }), original, 'status and token deltas do not trigger layout measurement');
  assert.deepEqual(dependencies([...bots].reverse()), original, 'server list order does not affect the displayed order');
  assert.notEqual(dependencies(bots, { selectedId: 'bot-2' })[0], original[0], 'selection moves the marker');
  assert.notEqual(dependencies([bots[0], { ...bots[1], chief: true }])[0], original[0], 'lead-bot reordering moves rows');
  assert.notEqual(dependencies(bots.slice(1))[0], original[0], 'archiving a row updates the roster layout');
});
