import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../../lib/motion-stub.mjs';
import { effortLabel } from '../../lib/chatStatus.ts';

const source = ts.transpileModule(readFileSync(new URL('./ModelPicker.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const settled = () => new Promise(resolve => setImmediate(resolve));
const fast = { id: 'fast', name: 'Fast', description: 'Faster inference' };
const standard = { id: 'standard', name: 'Standard', description: 'Standard inference' };

// Exercise the component's actual controls and PATCH requests. React's rendering
// machinery is stubbed; model-specific catalog values and handlers are real.
function picker({ model = {}, bot = {}, disabled = false, suspended = false, reducedMotion = false, updateGate, updateError } = {}) {
  let cursor = 0;
  let present = true;
  const values = [], requests = [], errors = [];
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const element = (type, props) => ({ type, props });
  const motion = motionTestModule();
  motion.useIsPresent = () => present;
  motion.useReducedMotion = () => reducedMotion;
  const props = {
    bot: { id: 'bot', backend: 'codex', model: 'current', effort: 'max', serviceTier: '', ...bot },
    capabilities: {
      models: [
        { id: 'current', name: 'Current', backend: 'codex', efforts: ['low', 'max', 'ultra'], serviceTiers: [standard, fast], defaultServiceTier: 'standard', ...model },
        { id: 'compatible', name: 'Compatible', backend: 'codex', efforts: ['max'], serviceTiers: [fast] },
        { id: 'other', name: 'Other', backend: 'pi', efforts: ['off', 'high'] },
      ],
      backends: { codex: { available: true }, pi: { available: true } },
    },
    disabled, suspended, onBotChange: updated => { props.bot = updated; },
    onOpenChange() {}, onError: error => errors.push(error),
  };
  const modules = {
    react: { useState: state, useRef: initial => state(() => ({ current: initial }))[0], useId: () => state('model-test')[0], useEffect() {}, createElement: (type, props, ...children) => element(type, { ...props, children }) },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../../lib/motion': motion,
    '../../lib/chatStatus': { effortLabel },
    '../../lib/api': { api: { updateBot: async (_, patch) => {
      requests.push(patch);
      if (updateGate) await updateGate;
      if (updateError) throw new Error(updateError);
      return { ...props.bot, ...patch };
    } }, errorMessage: error => error.message },
  };
  const exports = {};
  runInNewContext(source, {
    exports, require: name => modules[name], requestAnimationFrame: callback => callback(),
  }, { filename: 'ModelPicker.tsx' });
  const render = () => { cursor = 0; return exports.default(props); };
  function all(match) {
    const matches = [];
    function visit(node) {
      if (!node || typeof node !== 'object') return;
      if (match(node)) matches.push(node);
      const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children];
      for (const child of children.flat(Infinity)) visit(child);
    }
    visit(render());
    return matches;
  }
  const find = match => all(match)[0];
  return {
    props, requests, errors, find, all,
    present: next => { present = next; },
    surface: fields => exports.PresenceSurface(fields),
    open: () => find(node => node.props?.className === 'model-trigger').props.onClick(),
    fastToggle: () => find(node => node.props?.className === 'icon-button inference-fast-toggle'),
    toggleFast: async () => {
      find(node => node.props?.className === 'icon-button inference-fast-toggle').props.onClick();
      await settled();
    },
    choose: async name => {
      const switcher = find(node => node.props?.className === 'model-select-current');
      if (switcher) switcher.props.onClick();
      find(node => {
        if (node.props?.className !== 'model-choice') return false;
        const label = node.props.children[0].props.children.props.children;
        return (Array.isArray(label) ? label[0] : label) === name;
      }).props.onClick();
      await settled();
    },
  };
}

test('compact picker exposes model, effort and tier directly, then opens a discrete effort slider', () => {
  const view = picker({ bot: { serviceTier: 'fast' } });
  const trigger = view.find(node => node.props?.className === 'model-trigger');
  assert.match(trigger.props['aria-label'], /Model: Current/);
  assert.match(trigger.props['aria-label'], /Reasoning effort: Max/);
  assert.match(trigger.props['aria-label'], /Service tier: Fast/);
  view.open();
  const slider = view.find(node => node.type === 'input' && node.props.type === 'range');
  assert.equal(slider.props['aria-label'], 'Reasoning effort');
  assert.equal(slider.props.step, 1);
  assert.equal(slider.props.max, 2);
  assert.equal(slider.props['aria-valuetext'], 'Max');
  assert.equal(view.find(node => node.type === 'select' && node.props['aria-label'] === 'Reasoning effort'), undefined);
});

test('range pointer-up plus blur commits one effort change while model list retains supported tiers', async () => {
  const view = picker({ bot: { serviceTier: 'fast' } });
  view.open();
  let slider = view.find(node => node.type === 'input' && node.props.type === 'range');
  slider.props.onChange({ target: { value: '2' } });
  slider = view.find(node => node.type === 'input' && node.props.type === 'range');
  assert.equal(slider.props['aria-valuetext'], 'Ultra');
  slider.props.onPointerUp({ currentTarget: { value: '2' } });
  slider.props.onBlur({ currentTarget: { value: '2' } });
  await settled();
  assert.equal(view.requests.length, 1);
  assert.deepEqual(JSON.parse(JSON.stringify(view.requests[0])), { effort: 'ultra' });
  assert.equal(view.props.bot.serviceTier, 'fast');
});

test('keyboard range commits advertised efforts and rejects values outside the discrete catalog', async () => {
  const view = picker();
  view.open();
  const slider = view.find(node => node.type === 'input' && node.props.type === 'range');
  slider.props.onKeyUp({ key: 'Home', currentTarget: { value: '0' } });
  await settled();
  assert.equal(view.requests[0].effort, 'low');
  view.find(node => node.type === 'input' && node.props.type === 'range').props.onPointerUp({ currentTarget: { value: '99' } });
  await settled();
  assert.equal(view.requests.length, 1);
});

test('exiting popovers immediately become inert and lose modal semantics', () => {
  const view = picker();
  const active = view.surface({ modal: true, role: 'dialog', style: { color: 'red' } });
  assert.equal(active.props.inert, false);
  assert.equal(active.props['aria-hidden'], undefined);
  assert.equal(active.props['aria-modal'], true);

  view.present(false);
  const exiting = view.surface({ modal: true, role: 'dialog', style: { color: 'red' } });
  assert.equal(exiting.props.inert, true);
  assert.equal(exiting.props['aria-hidden'], true);
  assert.equal(exiting.props['aria-modal'], undefined);
  assert.equal(exiting.props.style.pointerEvents, 'none');
  assert.equal(exiting.props.style.color, 'red');
});

test('Fast service tier uses one lightning toggle, persists Fast and Auto, and has no separate dropdown', async () => {
  const view = picker();
  view.open();
  assert.equal(view.find(node => node.type === 'select'), undefined, 'the lightning is the sole tier control');
  assert.equal(view.fastToggle().type, 'button', 'a native button handles mouse, Enter and Space');
  assert.equal(view.fastToggle().props['aria-label'], 'Fast mode');
  assert.equal(view.fastToggle().props['aria-pressed'], false);
  assert.equal(view.fastToggle().props.disabled, false);
  assert.equal(view.fastToggle().props.children.type, 'Zap');
  await view.toggleFast();
  assert.deepEqual(JSON.parse(JSON.stringify(view.requests[0])), { serviceTier: 'fast' });
  assert.equal(view.fastToggle().props['aria-pressed'], true, 'settings stay open for the next preference');
  assert.equal(view.fastToggle().props.children.props.fill, 'currentColor', 'active Fast has a visible filled lightning');
  await view.toggleFast();
  assert.deepEqual(JSON.parse(JSON.stringify(view.requests[1])), { serviceTier: '' });
  assert.equal(view.fastToggle().props['aria-pressed'], false);
  assert.equal(view.fastToggle().props.children.props.fill, 'none');
});

test('the model heading is the only model-list entry', () => {
  const view = picker();
  view.open();
  assert.equal(view.find(node => node.props?.className === 'model-more-choice'), undefined,
    'there is no duplicate Choose model action below the slider');
  const heading = view.find(node => node.props?.className === 'model-select-current');
  assert.equal(heading.props.children[1].props.children[1].type, 'ChevronRight');
  heading.props.onClick();
  assert.equal(view.all(node => node.props?.className === 'model-choice').length, 3);
});

test('Ultra model rows have no lightning because Fast is an independent tier', () => {
  const view = picker();
  view.open();
  view.find(node => node.props?.className === 'model-select-current').props.onClick();
  assert.equal(view.all(node => node.type === 'Zap').length, 1,
    'only the compact inference trigger retains a lightning outside the model list');
  assert.equal(view.find(node => node.props?.['aria-label'] === 'Supports Ultra'), undefined);
});

test('Fast toggle uses the advertised Fast tier ID and ignores the automatic default tier', async () => {
  const view = picker({ model: { serviceTiers: [standard, { ...fast, id: 'priority' }], defaultServiceTier: 'priority' } });
  view.open();
  assert.equal(view.fastToggle().props['aria-pressed'], false, 'Auto never masquerades as an explicit Fast override');
  await view.toggleFast();
  assert.equal(view.requests[0].serviceTier, 'priority');
  assert.equal(view.fastToggle().props['aria-pressed'], true);
  const trigger = view.find(node => node.props?.className === 'model-trigger');
  assert.equal(trigger.props.children[0].props.fill, 'currentColor');
  await view.toggleFast();
  assert.equal(view.requests[1].serviceTier, '');
});

test('models without advertised Fast cannot enable it even when another tier is supported', async () => {
  for (const tiers of [undefined, [], [standard]]) {
    const view = picker({ model: { serviceTiers: tiers }, bot: { serviceTier: '' } });
    view.open();
    assert.equal(view.fastToggle().props.disabled, true);
    assert.match(view.fastToggle().props.title, /unavailable/i);
    await view.toggleFast();
    assert.equal(view.requests.length, 0);
    if (!tiers?.length) assert.equal(view.find(node => node.props?.className === 'model-current-tier'), undefined);
  }
});

test('a saved Codex tier can be reset when the model no longer advertises any tiers', async () => {
  const view = picker({ model: { serviceTiers: [] }, bot: { serviceTier: 'fast' } });
  view.open();
  assert.equal(view.fastToggle().props['aria-pressed'], true);
  assert.equal(view.fastToggle().props.disabled, false, 'an obsolete override can still be cleared');
  await view.toggleFast();
  assert.deepEqual(JSON.parse(JSON.stringify(view.requests[0])), { serviceTier: '' });
  assert.equal(view.fastToggle().props.disabled, true, 'after reset the unsupported setting cannot be re-enabled');
  assert.equal(view.find(node => node.props?.className === 'model-current-tier'), undefined);
});

test('a saved tier removed from the catalog remains visible until the user resets it', async () => {
  const view = picker({ bot: { serviceTier: 'retired-tier' } });
  view.open();
  assert.equal(view.find(node => node.props?.className === 'model-current-tier').props.children, 'retired-tier');
  assert.equal(view.fastToggle().props['aria-pressed'], false);
  view.find(node => node.props?.className === 'icon-button inference-reset').props.onClick();
  await settled();
  assert.equal(view.props.bot.serviceTier, '');
});

test('an unsupported saved tier can be cleared by the lightning when Fast is unavailable', async () => {
  const view = picker({ model: { serviceTiers: [] }, bot: { serviceTier: 'retired-tier' } });
  view.open();
  assert.equal(view.fastToggle().props.disabled, false);
  assert.match(view.fastToggle().props['aria-label'], /Clear unavailable service tier/);
  await view.toggleFast();
  assert.deepEqual(JSON.parse(JSON.stringify(view.requests[0])), { serviceTier: '' });
  assert.equal(view.fastToggle().props.disabled, true);
});

test('switching models keeps an accepted service tier and resets it for an unsupported backend', async () => {
  const view = picker({ bot: { serviceTier: 'fast' } });
  view.open();
  await view.choose('Compatible');
  assert.equal(view.requests[0].serviceTier, 'fast');
  view.open();
  await view.choose('Other');
  assert.equal(view.requests[1].backend, 'pi');
  assert.equal(view.requests[1].serviceTier, '');
  assert.equal(view.requests[1].effort, 'high');
});

test('disabled, suspended, exiting and saving inference controls cannot change Fast tier', async () => {
  const disabled = picker({ disabled: true }); disabled.open();
  assert.equal(disabled.fastToggle().props.disabled, true);
  await disabled.toggleFast();
  assert.equal(disabled.requests.length, 0);

  const suspended = picker({ suspended: true }); suspended.open();
  assert.equal(suspended.fastToggle(), undefined);

  const exiting = picker(); exiting.open();
  exiting.present(false);
  assert.equal(exiting.fastToggle(), undefined);
  assert.equal(exiting.requests.length, 0);

  let release;
  const view = picker({ updateGate: new Promise(resolve => { release = resolve; }) });
  view.open();
  const toggle = view.fastToggle();
  toggle.props.onClick(); toggle.props.onClick();
  assert.equal(view.fastToggle().props.disabled, true);
  assert.equal(view.requests.length, 1, 'repeated clicks cannot race a pending preference update');
  release(); await settled();
  assert.equal(view.fastToggle().props.disabled, false);
  assert.equal(view.fastToggle().props['aria-pressed'], true);
});

test('a failed Fast update retains the confirmed tier and reports the error', async () => {
  const view = picker({ updateError: 'Cannot update inference' });
  view.open();
  await view.toggleFast();
  assert.equal(view.fastToggle().props['aria-pressed'], false);
  assert.equal(view.fastToggle().props.disabled, false);
  assert.deepEqual(view.errors, ['Cannot update inference']);
});

test('reduced motion keeps the native Fast toggle and disables the trigger-chevron animation', async () => {
  const view = picker({ reducedMotion: true });
  view.open();
  const chevron = view.find(node => node.props?.className === 'model-trigger-chevron');
  assert.equal(chevron.props.transition.duration, 0);
  await view.toggleFast();
  assert.equal(view.fastToggle().props['aria-pressed'], true);
});
