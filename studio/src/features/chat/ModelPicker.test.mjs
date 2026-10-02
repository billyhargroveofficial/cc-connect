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
function picker({ model = {}, bot = {}, disabled = false } = {}) {
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
    disabled, onBotChange: updated => { props.bot = updated; },
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
    tier: () => find(node => node.type === 'select' && node.props['aria-label'] === 'Service tier'),
    changeTier: async value => {
      find(node => node.type === 'select' && node.props['aria-label'] === 'Service tier').props.onChange({ target: { value } });
      await settled();
    },
    choose: async name => {
      const switcher = find(node => node.props?.className === 'model-select-current');
      if (switcher) switcher.props.onClick();
      find(node => node.props?.className === 'model-choice' && node.props.children[0].props.children.props.children[0] === name).props.onClick();
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

test('service tier uses the current model catalog and persists explicit and automatic choices', async () => {
  const view = picker();
  view.open();
  assert.equal(view.tier().props.value, '');
  assert.deepEqual(view.all(node => node.type === 'option').map(node => node.props.value).slice(-3), ['', 'standard', 'fast']);
  await view.changeTier('fast');
  assert.deepEqual(JSON.parse(JSON.stringify(view.requests[0])), { serviceTier: 'fast' });
  assert.equal(view.tier().props.value, 'fast', 'settings stay open for the next preference');
  await view.changeTier('');
  assert.deepEqual(JSON.parse(JSON.stringify(view.requests[1])), { serviceTier: '' });
  assert.equal(view.tier().props.value, '');
});

test('a model without advertised service tiers has no tier control', () => {
  const view = picker({ model: { serviceTiers: undefined }, bot: { serviceTier: '' } });
  view.open();
  assert.equal(view.tier(), undefined);
  assert.equal(view.find(node => node.props?.className === 'model-current-tier'), undefined);
});

test('a saved Codex tier can be reset when the model no longer advertises any tiers', async () => {
  const view = picker({ model: { serviceTiers: [] }, bot: { serviceTier: 'fast' } });
  view.open();
  assert.equal(view.tier().props.value, 'fast');
  assert.deepEqual(view.all(node => node.type === 'option').map(node => node.props.value).slice(-2), ['', 'fast']);
  await view.changeTier('');
  assert.deepEqual(JSON.parse(JSON.stringify(view.requests[0])), { serviceTier: '' });
  assert.equal(view.tier(), undefined, 'after reset the unsupported setting disappears');
});

test('a saved tier removed from the catalog remains visible until the user resets it', async () => {
  const view = picker({ bot: { serviceTier: 'retired-tier' } });
  view.open();
  assert.equal(view.tier().props.value, 'retired-tier');
  const unavailable = view.find(node => node.type === 'option' && node.props.value === 'retired-tier');
  assert.equal(unavailable.props.disabled, true);
  await view.changeTier('');
  assert.equal(view.tier().props.value, '');
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

test('disabled settings and values outside the model catalog cannot change service tier', async () => {
  const disabled = picker({ disabled: true });
  disabled.open();
  await disabled.changeTier('fast');
  assert.equal(disabled.requests.length, 0);
  const view = picker();
  view.open();
  await view.changeTier('invented-tier');
  assert.equal(view.requests.length, 0);
});
