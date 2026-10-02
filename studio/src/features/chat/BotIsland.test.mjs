import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../../lib/motion-stub.mjs';

const source = ts.transpileModule(readFileSync(new URL('./BotIsland.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function findIn(root, match) {
  if (!root || typeof root !== 'object') return undefined;
  if (match(root)) return root;
  const children = Array.isArray(root.props?.children) ? root.props.children : [root.props?.children];
  for (const child of children.flat(Infinity)) {
    const result = findIn(child, match);
    if (result) return result;
  }
}

// Exercise the island's actual open/close handlers. JSX and hooks are stubbed;
// preserving the host card's position/ref is also checked in the browser.
function island({ desktop = true, shellPresent = true, panePresent = true } = {}) {
  let cursor = 0, focusTrap, effects = [], callbacks = [];
  const values = [], calls = [], timers = [];
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const rendering = [];
  const element = (type, props) => {
    if (typeof type !== 'function') return { type, props };
    rendering.push(type.name);
    try { return type(props); } finally { rendering.pop(); }
  };
  const modules = {
    react: { memo: component => component,
      useState: state,
      useRef: initial => state(() => ({ current: initial }))[0],
      useEffect(effect) { effects.push(effect); },
      useMemo: value => value(),
      useCallback: (callback, dependencies) => { callbacks.push({ callback, dependencies }); return callback; },
    },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../../components/Avatar': { default: 'Avatar' },
    '../../lib/api': { fileURL: () => '/file' },
    '../../lib/motion': { ...motionTestModule(), useIsPresent: () => rendering.at(-1) === 'IslandPane' ? panePresent : shellPresent },
    '../../lib/events': {
      botMessagePresentation: () => undefined,
      telegramLabel: () => 'Connected',
      telegramTitle: () => 'Connected',
    },
    '../settings/SettingsDrawer': { BotSettingsPanel: 'BotSettingsPanel' },
    './ModelPicker': { useDialogFocus: (active, dialog, onClose) => { focusTrap = { active, dialog, onClose }; } },
    './BotChangeHistory': { default: 'BotChangeHistory' },
    './bot-island.css': {},
  };
  const exports = {};
  runInNewContext(source, {
    exports,
    require: name => {
      assert.ok(name in modules, `Unexpected BotIsland import: ${name}`);
      return modules[name];
    },
    window: {
      matchMedia: () => ({ matches: desktop, addEventListener() {}, removeEventListener() {} }),
      requestAnimationFrame: callback => callback(),
      setTimeout: callback => { timers.push(callback); return timers.length; },
      clearTimeout() {},
    },
    document: { activeElement: null },
  }, { filename: 'BotIsland.tsx' });
  const props = {
    bot: { id: 'bot-1', name: 'Researcher', status: 'idle', role: 'Find primary sources.' },
    events: [], history: [], status: 'Ready', working: false,
    supportsGoal: false, hasGoal: false,
    open: !desktop,
    suspended: false,
    onOpen: () => { props.open = true; },
    triggerRef: { current: { focus() {} } },
    capabilities: null,
    onGoal: () => calls.push('goal'),
    onClose: () => { calls.push('close-island'); props.open = false; },
    onBotChange: bot => calls.push(['update', bot]),
    onArchive: id => calls.push(['archive', id]),
    // The old implementation delegated settings to an external drawer.
    onSettings: () => calls.push('external-settings'),
  };
  const render = () => { cursor = 0; effects = []; callbacks = []; return exports.default(props); };
  const find = match => findIn(render(), match);
  const card = () => find(node => node.props?.className === 'bot-island-card');
  const panel = () => find(node => node.type === 'BotSettingsPanel');
  return {
    calls, props, card, panel, focusWithoutScroll: exports.focusWithoutScroll,
    shell: () => find(node => node.props?.className?.split(' ').includes('bot-island-shell')),
    pane: () => find(node => ['bot-island-overview', 'bot-island-settings'].includes(node.props?.className)),
    flushEffects: () => { render(); effects.forEach(effect => effect()); },
    openSettings: () => { render(); callbacks.find(entry => entry.dependencies.includes(props.onOpen)).callback(); },
    focusTrap: () => { render(); return focusTrap; },
    settings: () => find(node => node.props?.['aria-label'] === 'Bot settings' && node.type === 'button').props.onClick(),
  };
}

for (const desktop of [true, false]) {
  test(`bot settings expand inside the existing ${desktop ? 'desktop' : 'mobile'} island and return to its overview`, () => {
    const view = island({ desktop });
    const cardRef = view.card().props.ref;
    assert.equal(view.panel(), undefined);
    view.settings();
    assert.equal(view.props.open, !desktop, 'desktop settings do not arm the mobile overlay state');

    assert.ok(view.panel(), 'Settings must appear inside the island');
    const expandedCard = view.card();
    assert.ok(findIn(expandedCard, node => node.type === 'BotSettingsPanel'),
      'the settings panel belongs to the existing card');
    assert.equal(view.card().props.ref, cardRef, 'the host card ref survives expansion');
    assert.equal(view.panel().props.bot, view.props.bot);
    assert.equal(view.panel().props.onBotChange, view.props.onBotChange);
    assert.equal(view.panel().props.onArchive, view.props.onArchive);
    assert.deepEqual(view.calls, [], 'opening settings neither closes the island nor opens an external drawer');
    if (!desktop) {
      assert.equal(view.props.open, true);
      assert.equal(view.card().props.role, 'dialog');
      assert.equal(view.focusTrap().active, true);
    }

    view.panel().props.onClose();
    assert.equal(view.panel(), undefined);
    assert.equal(view.card().props.ref, cardRef, 'returning to overview preserves the card');
    assert.equal(view.shell().props['aria-label'], 'Bot details');
    assert.deepEqual(view.calls, [], 'Back collapses settings without closing the island');
    view.settings();
    assert.ok(view.panel(), 'settings can be opened again after collapsing');
  });
}

test('opening settings only requests the bot details overlay on mobile', () => {
  const desktop = island();
  desktop.openSettings();
  assert.equal(desktop.props.open, false, 'desktop settings must not survive resize as an open mobile overlay');
  const mobile = island({ desktop: false });
  mobile.props.open = false;
  mobile.openSettings();
  assert.equal(mobile.props.open, true, 'the mobile settings controller opens the host details overlay');
  assert.ok(mobile.panel());
});

test('settings transition stays pinned to the island right edge and focus never scrolls it', () => {
  const view = island();
  const panePresence = findIn(view.card(), node => node.props?.mode === 'popLayout');
  assert.ok(panePresence, 'overview and settings use one presence switch');
  assert.equal(panePresence.props.anchorX, 'right', 'the popped overview follows the card fixed edge');
  assert.equal(view.pane().props.layout, undefined, 'pane crossfades must not start a second layout projection');

  const calls = [];
  const target = { focus: options => calls.push(options) };
  view.focusWithoutScroll(target);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].preventScroll, true);
});

test('bot overview identifies its host in one muted line', () => {
  const view = island();
  view.props.node = { id: 'mac', name: 'MacBook', hostname: 'Billy-Mac', os: 'darwin', online: false };
  const line = findIn(view.card(), node => node.props?.className === 'bot-island-host');
  assert.ok(line);
  assert.equal(line.props.title, 'MacBook · Billy-Mac · darwin');
  assert.equal(findIn(line, node => node.type === 'span').props.children, 'MacBook');
  assert.equal(findIn(line, node => node.type === 'small').props.children, 'Offline');
  view.props.node.online = true;
  assert.equal(findIn(view.card(), node => node.type === 'small' && node.props?.children === 'Offline'), undefined);
});

test('lifecycle history belongs to the existing bot island overview', () => {
  const view = island();
  const history = [{ seq: 1, botId: 'bot-1', type: 'system', data: {} }];
  view.props.history = history;
  const section = findIn(view.card(), node => node.type === 'BotChangeHistory');
  assert.ok(section);
  assert.equal(section.props.events, history);
  assert.equal(section.props.botId, view.props.bot.id);
  view.settings();
  assert.equal(findIn(view.card(), node => node.type === 'BotChangeHistory'), undefined);
  view.panel().props.onClose();
  assert.equal(findIn(view.card(), node => node.type === 'BotChangeHistory').props.events, history);
});

test('mobile Escape first returns from bot settings to details, then closes details', () => {
  const view = island({ desktop: false });
  view.settings();
  view.focusTrap().onClose();
  assert.equal(view.panel(), undefined);
  assert.equal(view.props.open, true);
  assert.deepEqual(view.calls, []);
  view.focusTrap().onClose();
  assert.equal(view.props.open, false);
  assert.deepEqual(view.calls, ['close-island']);
});

for (const settings of [false, true]) {
  test(`exiting island ${settings ? 'settings' : 'overview'} immediately blocks interaction and assistive technology`, () => {
    const active = island();
    if (settings) active.settings();
    assert.equal(active.shell().props.inert, false);
    assert.equal(active.pane().props.inert, false);
    assert.equal(active.pane().props['aria-hidden'], undefined);

    const exiting = island({ panePresent: false });
    if (settings) exiting.settings();
    assert.equal(exiting.shell().props.inert, false, 'only the closing pane is disabled, so the new pane can remain active');
    assert.equal(exiting.pane().props.inert, true);
    assert.equal(exiting.pane().props['aria-hidden'], true);
    assert.ok(settings ? exiting.panel() : exiting.pane(), 'the closing pane remains mounted only for its exit animation');
  });
}

test('exiting mobile island shell immediately becomes inert and hidden even while its last open props are retained', () => {
  const exiting = island({ desktop: false, shellPresent: false });
  assert.equal(exiting.props.open, true, 'AnimatePresence retains the last open props during exit');
  assert.equal(exiting.shell().props.inert, true);
  assert.equal(exiting.shell().props['aria-hidden'], true);
  assert.equal(exiting.shell().props.style.pointerEvents, 'none');
});

for (const desktop of [true, false]) {
  test(`shared modal suspension clears ${desktop ? 'desktop' : 'mobile'} island settings before returning`, () => {
    const view = island({ desktop });
    view.settings();
    assert.ok(view.panel());
    view.props.suspended = true;
    view.flushEffects();
    assert.equal(view.panel(), undefined, 'the suspended island drops the active settings view');
    view.props.suspended = false;
    view.flushEffects();
    assert.equal(view.panel(), undefined, 'closing the shared modal must not reopen old settings');
    assert.deepEqual(view.calls, [], 'clearing settings does not close the bot island or affect the bot');
  });
}
