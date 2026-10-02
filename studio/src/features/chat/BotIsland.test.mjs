import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';

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
function island({ desktop = true } = {}) {
  let cursor = 0, focusTrap;
  const values = [], calls = [], timers = [];
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const element = (type, props) => ({ type, props });
  const modules = {
    react: {
      useState: state,
      useRef: initial => state(() => ({ current: initial }))[0],
      useEffect() {},
      useMemo: value => value(),
      useCallback: callback => callback,
    },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../../components/Avatar': { default: 'Avatar' },
    '../../lib/api': { fileURL: () => '/file' },
    '../../lib/events': {
      botMessagePresentation: () => undefined,
      telegramLabel: () => 'Connected',
      telegramTitle: () => 'Connected',
    },
    '../settings/SettingsDrawer': { BotSettingsPanel: 'BotSettingsPanel' },
    './ModelPicker': { useDialogFocus: (active, dialog, onClose) => { focusTrap = { active, dialog, onClose }; } },
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
      matchMedia: () => ({ matches: desktop }),
      requestAnimationFrame: callback => callback(),
      setTimeout: callback => { timers.push(callback); return timers.length; },
      clearTimeout() {},
    },
  }, { filename: 'BotIsland.tsx' });
  const props = {
    bot: { id: 'bot-1', name: 'Researcher', status: 'idle', role: 'Find primary sources.' },
    events: [], status: 'Ready', working: false,
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
  const render = () => { cursor = 0; return exports.default(props); };
  const find = match => findIn(render(), match);
  const card = () => find(node => node.props?.className === 'bot-island-card');
  const panel = () => find(node => node.type === 'BotSettingsPanel');
  return {
    calls, props, card, panel,
    shell: () => render(),
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

    assert.ok(view.panel(), 'Settings must appear inside the island');
    const expandedCard = view.card();
    assert.equal(findIn(expandedCard, node => node.type === 'BotSettingsPanel'), expandedCard.props.children,
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
