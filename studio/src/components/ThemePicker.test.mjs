import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';

const source = ts.transpileModule(readFileSync(new URL('./ThemePicker.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function picker(initial = 'system') {
  let cursor = 0;
  const values = [];
  const changes = [];
  const state = initialValue => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initialValue === 'function' ? initialValue() : initialValue;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const element = (type, props) => ({ type, props });
  const props = {
    value: initial,
    onChange: value => { changes.push(value); props.value = value; },
  };
  const modules = {
    react: {
      useState: state,
      useRef: initialValue => state(() => ({ current: initialValue }))[0],
      useEffect() {},
    },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
  };
  const exports = {};
  runInNewContext(source, {
    exports,
    require: name => modules[name],
    requestAnimationFrame: callback => callback(),
    cancelAnimationFrame() {},
  }, { filename: 'ThemePicker.tsx' });
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
  const root = () => find(node => node.props?.className === 'theme-picker');
  const trigger = () => find(node => node.props?.['aria-haspopup'] === 'menu');
  const option = label => find(node => node.props?.role === 'menuitemradio'
    && node.props.children[1].props.children === label);
  return {
    changes,
    root,
    trigger,
    option,
    options: () => all(node => node.props?.role === 'menuitemradio'),
    open: () => trigger().props.onClick(),
    choose: label => option(label).props.onClick(),
  };
}

test('theme picker exposes system, light and dark as explicit choices', () => {
  const view = picker();
  assert.equal(view.trigger().props['aria-label'], 'Theme: System');
  view.open();
  assert.deepEqual(view.options().map(node => node.props.children[1].props.children), [
    'System', 'Light', 'Dark',
  ]);
  assert.equal(view.option('System').props['aria-checked'], true);
});

test('theme picker passes each selected preference and reflects it on reopen', () => {
  const view = picker();
  view.open();
  view.choose('Light');
  assert.deepEqual(view.changes, ['light']);
  assert.equal(view.trigger().props['aria-label'], 'Theme: Light');
  view.open();
  assert.equal(view.option('Light').props['aria-checked'], true);
  view.choose('Dark');
  assert.deepEqual(view.changes, ['light', 'dark']);
});

test('theme picker closes when keyboard focus leaves the control', () => {
  const view = picker();
  view.open();
  assert.equal(view.options().length, 3);
  view.root().props.onBlur({
    currentTarget: { contains: () => false },
    relatedTarget: {},
  });
  assert.equal(view.options().length, 0);
});
