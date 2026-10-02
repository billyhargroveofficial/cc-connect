import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../../lib/motion-stub.mjs';

const source = ts.transpileModule(readFileSync(new URL('./SkillPicker.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
function picker({ reduced = false, status = 'ready' } = {}) {
  const hooks = [], effects = [], listeners = new Map(), chosen = [], highlighted = [], motion = motionTestModule();
  let cursor = 0, closed = 0, retried = 0;
  const element = (type, props) => ({ type, props });
  const modules = {
    react: { memo: component => component, useRef: initial => hooks[cursor++] ||= { current: initial }, useEffect: effect => effects.push(effect) },
    'react/jsx-runtime': { jsx: element, jsxs: element },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../../lib/motion': { ...motion, useReducedMotion: () => reduced },
    './ModelPicker': { PresenceSurface: 'surface' }, './skill-picker.css': {},
  };
  const exports = {};
  runInNewContext(source, { exports, require: name => { assert.ok(name in modules); return modules[name]; }, document: {
    addEventListener: (type, callback) => listeners.set(type, callback), removeEventListener: (type, callback) => { if (listeners.get(type) === callback) listeners.delete(type); },
  } });
  const props = { id: 'skills', index: 1, status, error: 'Temporary failure', skills: [
    { id: 'shared', name: 'review', description: 'Review source files.', scope: 'productUser', path: '/private/shared/SKILL.md' },
    { id: 'project', name: 'review', description: 'Check this bot’s output.', scope: 'project', path: '/private/project/SKILL.md' },
  ], onChoose: skill => chosen.push(skill), onHighlight: index => highlighted.push(index), onClose: () => closed++, onRetry: () => retried++ };
  cursor = 0;
  const tree = exports.default(props);
  const cleanups = effects.splice(0).map(effect => effect());
  const nodes = [];
  const visit = node => { if (!node || typeof node !== 'object') return; nodes.push(node); const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children]; children.flat(Infinity).forEach(visit); };
  visit(tree);
  return { tree, motion, props, chosen, highlighted, listeners, closed: () => closed, retried: () => retried,
    all: match => nodes.filter(match), unmount: () => cleanups.forEach(cleanup => cleanup?.()) };
}

test('skill suggestions expose a listbox with scoped options while mouse selection retains textarea focus', () => {
  const view = picker();
  assert.equal(view.tree.props.role, 'listbox');
  assert.equal(view.tree.props['aria-modal'], undefined, 'autocomplete does not trap typing in a dialog');
  const options = view.all(node => node.props?.role === 'option');
  assert.deepEqual(options.map(node => [node.props.id, node.props['aria-selected']]), [['skills-option-0', false], ['skills-option-1', true]]);
  const scopes = view.all(node => node.type === 'small').map(node => node.props.children);
  assert.deepEqual(scopes, ['Shared', 'Project']);
  let prevented = false;
  options[0].props.onMouseDown({ preventDefault: () => prevented = true });
  assert.equal(prevented, true);
  options[0].props.onClick(); assert.equal(view.chosen[0], view.props.skills[0]);
  options[1].props.onPointerMove({ pointerType: 'mouse' });
  options[0].props.onPointerMove({ pointerType: 'touch' });
  assert.deepEqual(view.highlighted, [1], 'touch scrolling never changes the keyboard selection');
  view.tree.props.ref.current = { contains: target => target.inside === true };
  const outside = view.listeners.get('pointerdown');
  outside({ target: { inside: true } }); outside({ target: { tagName: 'TEXTAREA' } });
  assert.equal(view.closed(), 0);
  outside({ target: { tagName: 'BUTTON' } }); assert.equal(view.closed(), 1);
  view.unmount(); assert.equal(view.listeners.size, 0);
});

test('reduced motion uses the shared fade and retry remains a local action', () => {
  const view = picker({ reduced: true, status: 'error' });
  assert.equal(view.tree.props.variants, view.motion.fade);
  assert.equal(view.all(node => node.props?.role === 'option').length, 0);
  view.all(node => node.type === 'button')[0].props.onClick();
  assert.equal(view.retried(), 1);
  const css = readFileSync(new URL('./skill-picker.css', import.meta.url), 'utf8');
  assert.match(css, /\.skill-picker\s*\{[^}]*border:\s*0;/);
  assert.match(css, /\.skill-picker-option\s*\{[^}]*min-height:\s*48px;/);
  assert.match(css, /@media \(max-width: 700px\)/);
  view.unmount();
});
