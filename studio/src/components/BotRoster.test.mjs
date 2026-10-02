import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../lib/motion-stub.mjs';
import * as hostCatalog from '../lib/hostCatalog.ts';

const source = ts.transpileModule(readFileSync(new URL('./BotRoster.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function renderRoster(bots, onCreate, overrides = {}) {
  const element = (type, props) => typeof type === 'function' ? type(props) : ({ type, props });
  const modules = {
    react: { memo: component => component, useMemo: factory => factory(), useEffect() {}, useRef: initial => ({ current: initial }), useState: initial => [typeof initial === 'function' ? initial() : initial, () => {}], ...overrides.hooks },
    'react/jsx-runtime': { jsx: element, jsxs: element },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../lib/events': {
      isWorking: () => false,
      messagePreview: events => events.at(-1)?.data?.content || '',
      statusLabel: () => 'Ready',
      telegramTitle: () => 'Connected',
    },
    './Avatar': { default: 'Avatar' },
    './SidebarResizeHandle': { default: 'SidebarResizeHandle' },
    './ThemePicker': { default: 'ThemePicker' },
    '../lib/motion': { ...motionTestModule(), useIsPresent: () => overrides.present !== false },
    '../lib/hostCatalog': hostCatalog,
  };
  const exports = {};
  runInNewContext(source, {
    exports,
    requestAnimationFrame: callback => { callback(); return 1; },
    require: name => {
      assert.ok(name in modules, `Unexpected BotRoster import: ${name}`);
      return modules[name];
    },
  }, { filename: 'BotRoster.tsx' });
  return exports.default({
    bots: bots.map(bot => bot.bot ? bot : ({ bot, node: { id: 'local', name: 'This server', local: true, online: true }, key: hostCatalog.catalogBotKey('local', bot.id) })),
    events: {}, selectedKey: hostCatalog.catalogBotKey('local', overrides.selectedId || ''), onCreate,
    onSelect() {}, onSettings() {}, onTheme() {}, onLogout() {},
    hostFilters: { server: true, mac: true }, onFilterChange() {},
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

test('the account identity shares the existing connection line', () => {
  const roster = renderRoster([], () => {}, { user: { id: 'user-1', username: 'billy.name' } });
  const line = allIn(roster, node => node.props.className === 'roster-connection')[0];
  const identity = allIn(line, node => node.props.className === 'roster-connection-copy')[0];
  assert.equal(identity.props.children, '@billy.name · Connected');
  assert.equal(line.props.title, 'Signed in as @billy.name · Workspace connected');
  assert.equal(allIn(roster, node => node.props.className === 'roster-connection').length, 1);
});

test('an offline active device shares the existing connection line without a host dropdown', () => {
  const node = { id: 'mac', name: 'MacBook', online: false, local: false };
  const roster = renderRoster([], () => {}, { activeNode: node, user: { id: 'user-1', username: 'billy' } });
  const line = allIn(roster, node => node.props?.className === 'roster-connection')[0];
  assert.equal(line.props.title, 'Signed in as @billy · MacBook · Offline');
  assert.equal(allIn(line, node => node.props?.className === 'roster-connection-copy')[0].props.children, '@billy · Offline');
  assert.equal(allIn(roster, node => node.type === 'header').length, 0);
  assert.equal(allIn(roster, node => node.props?.className?.split(' ').includes('roster-connection')).length, 1);
  assert.equal(allIn(roster, node => node.props?.['aria-haspopup'] === 'menu').length, 0);
});

test('native Server and Mac checkboxes filter rows independently without selecting another host', () => {
  const local = { id: 'local', name: 'Server', online: true, local: true };
  const mac = { id: 'mac', name: 'MacBook', online: false, local: false, os: 'darwin' };
  const entries = [local, mac].map(node => ({ key: hostCatalog.catalogBotKey(node.id, 'shared'), node,
    bot: { id: 'shared', name: node.local ? 'Server bot' : 'Mac bot', createdAt: '2026-10-02', status: 'idle' } }));
  const selections = [], filters = { server: true, mac: true };
  const overrides = { activeNode: local, hostFilters: filters,
    onFilterChange: (category, enabled) => { filters[category] = enabled; },
    onSelect: (id, nodeId) => selections.push([id, nodeId]) };
  const render = () => renderRoster(entries, () => {}, overrides);
  const rows = () => allIn(render(), node => node.props?.className?.startsWith('bot-row '));
  const checks = () => allIn(render(), node => node.type === 'input' && node.props.type === 'checkbox');
  assert.equal(rows().length, 2);
  assert.deepEqual(checks().map(check => check.props.checked), [true, true]);
  checks()[0].props.onChange({ target: { checked: false } });
  assert.equal(rows().length, 1);
  assert.equal(allIn(rows()[0], node => node.props?.className === 'bot-row-name')[0].props.children, 'Mac bot');
  assert.deepEqual(selections, [], 'filtering does not change the active host or conversation');
  rows()[0].props.onClick();
  assert.deepEqual(selections, [['shared', 'mac']], 'offline cached rows remain selectable with their explicit node');
  checks()[1].props.onChange({ target: { checked: false } });
  assert.equal(rows().length, 0);
  assert.equal(allIn(render(), node => node.props?.className === 'roster-empty')[0].props.children, 'No bots on the selected devices.');
  checks()[0].props.onChange({ target: { checked: true } });
  assert.equal(rows().length, 1);
  assert.equal(allIn(rows()[0], node => node.props?.className === 'bot-row-name')[0].props.children, 'Server bot');
});

test('duplicate bot IDs have separate selection and device labels and do not borrow active-host previews', () => {
  const local = { id: 'local', name: 'This server', local: true, online: true };
  const mac = { id: 'mac', name: 'MacBook', local: false, online: true, os: 'darwin' };
  const entries = [local, mac].map(node => ({ key: hostCatalog.catalogBotKey(node.id, 'shared'), node,
    bot: { id: 'shared', name: node.name, createdAt: '2026-10-02', status: 'idle', role: 'Own role' } }));
  const roster = renderRoster(entries, () => {}, { activeNode: local, selectedKey: entries[1].key,
    events: { shared: [{ type: 'message', data: { content: 'Private server preview' } }] } });
  const rows = allIn(roster, node => node.props?.className?.startsWith('bot-row '));
  const rowFor = name => rows.find(row => allIn(row, node => node.props?.className === 'bot-row-name')[0].props.children === name);
  assert.equal(rowFor(local.name).props['aria-current'], undefined);
  assert.equal(rowFor(mac.name).props['aria-current'], 'page');
  assert.equal(allIn(rowFor(local.name), node => node.props?.className === 'bot-row-device')[0].props.children[1], 'Server');
  assert.equal(allIn(rowFor(mac.name), node => node.props?.className === 'bot-row-device')[0].props.children[1], 'MacBook');
  assert.equal(allIn(rowFor(local.name), node => node.props?.className?.startsWith('bot-row-preview'))[0].props.children[1], 'Private server preview');
  assert.equal(allIn(rowFor(mac.name), node => node.props?.className?.startsWith('bot-row-preview'))[0].props.children[1], 'Own role');
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
