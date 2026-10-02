import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../lib/motion-stub.mjs';

const source = ts.transpileModule(`${readFileSync(new URL('./Login.tsx', import.meta.url), 'utf8')}\nexport { LoginNotice, LoginConfirmation };`, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function fixture(overrides = {}) {
  const slots = [];
  let index = 0;
  let dirty = false;
  let effects = [];
  const react = {
    useState(initial) {
      const i = index++;
      if (!(i in slots)) slots[i] = typeof initial === 'function' ? initial() : initial;
      return [slots[i], next => {
        const value = typeof next === 'function' ? next(slots[i]) : next;
        if (!Object.is(value, slots[i])) { slots[i] = value; dirty = true; }
      }];
    },
    useRef(value) {
      const i = index++;
      return slots[i] ??= { current: value };
    },
    useEffect(effect, deps) {
      const i = index++;
      if (!slots[i] || deps.some((value, j) => !Object.is(value, slots[i][j]))) {
        slots[i] = deps;
        effects.push(effect);
      }
    },
  };
  const element = (type, props) => typeof type === 'function' ? type(props) : ({ type, props });
  const modules = {
    react,
    'react/jsx-runtime': { jsx: element, jsxs: element },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    './Brand': { default: 'Brand' },
    './Avatar': { default: 'Avatar' },
    '../lib/api': { errorMessage: error => error.message },
    '../lib/motion': {
      ...motionTestModule(),
      useIsPresent: () => overrides.present !== false,
      useReducedMotion: () => !!overrides.reduced,
      motionTransition: { ...motionTestModule().motionTransition, disclosure: { duration: 0.19 } },
    },
  };
  const exports = {};
  runInNewContext(source, { exports, TextEncoder, require: name => modules[name] }, { filename: 'Login.tsx' });
  let props = {
    checking: false, error: '', registrationAllowed: true,
    onLogin: async () => {}, onRegister: async () => {}, onRetry() {},
    ...overrides,
  };
  let tree;
  function render() {
    do {
      dirty = false;
      index = 0;
      effects = [];
      tree = exports.default(props);
      for (const effect of effects) effect();
    } while (dirty);
    return tree;
  }
  function all(match) {
    render();
    const nodes = [];
    function visit(node) {
      if (!node || typeof node !== 'object') return;
      if (match(node)) nodes.push(node);
      const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children];
      for (const child of children.flat(Infinity)) visit(child);
    }
    visit(tree);
    return nodes;
  }
  function text(node) {
    if (node == null || node === false) return '';
    if (typeof node !== 'object') return String(node);
    return [node.props?.children].flat(Infinity).map(text).join('');
  }
  return {
    exports,
    render,
    all,
    text,
    update(next) { props = { ...props, ...next }; render(); },
    field: id => all(node => node.props?.id === `account-${id}`)[0],
    edit(id, value) {
      all(node => node.props?.id === `account-${id}`)[0].props.onChange({ target: { value } });
      render();
    },
    tab(mode) { all(node => node.props?.id === `account-${mode}-tab`)[0].props.onClick(); render(); },
    submit: () => all(node => node.type === 'form')[0].props.onSubmit({ preventDefault() {} }),
    message: () => text(all(node => node.props?.role === 'alert')[0]),
  };
}

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}

test('login notices reveal immediately when reduced motion is requested', () => {
  const normal = fixture().exports.LoginNotice({ message: 'Unable to connect.' });
  const reduced = fixture({ reduced: true }).exports.LoginNotice({ message: 'Unable to connect.' });
  assert.equal(normal.props.transition.duration, 0.19);
  assert.equal(reduced.props.transition.duration, 0);
});

test('account validation accepts canonical dot usernames and checks actual UTF-8 password bytes', () => {
  const validate = fixture().exports.validateAccountInput;
  assert.equal(validate(' Billy.Name ', 'abcdefgh'), '');
  assert.equal(validate('billy', 'é'.repeat(64)), '');
  assert.match(validate('billy', 'é'.repeat(65)), /128 bytes/);
  assert.match(validate('billy', 'short'), /8 and 128/);
  assert.match(validate('.billy', 'abcdefgh'), /Start with/);
  assert.match(validate('billy name', 'abcdefgh'), /letters/);
});

test('sign in sends username and the exact password once while pending', async () => {
  const pending = deferred();
  const calls = [];
  const view = fixture({ onLogin: credentials => { calls.push(credentials); return pending.promise; } });
  view.edit('username', ' Billy.Name ');
  view.edit('password', '  pass.word  ');
  assert.equal(view.field('password').props.autoComplete, 'current-password');
  assert.equal(view.field('username').props.autoComplete, 'username');
  const submitted = view.submit();
  await view.submit();
  assert.equal(calls.length, 1);
  assert.equal(calls[0].username, 'billy.name');
  assert.equal(calls[0].password, '  pass.word  ', 'password spaces are never trimmed');
  assert.equal(view.field('username').props.disabled, true);
  assert.equal(view.field('password').props.disabled, true);
  const submit = view.all(node => node.type === 'button' && node.props.type === 'submit')[0];
  assert.equal(submit.props['aria-busy'], true);
  assert.equal(submit.props['aria-label'], 'Signing in');
  pending.resolve();
  await submitted;
  assert.equal(view.field('password').props.value, '');
});

test('create account rejects mismatched passwords locally and sends no confirmation field', async () => {
  const calls = [];
  const view = fixture({ onRegister: async credentials => calls.push(credentials) });
  view.tab('register');
  view.edit('username', 'new.user');
  view.edit('password', ' pass word ');
  view.edit('confirmation', 'different');
  await view.submit();
  assert.equal(calls.length, 0);
  assert.match(view.message(), /do not match/);
  assert.equal(view.field('confirmation').props['aria-invalid'], true);
  assert.equal(view.field('confirmation').props['aria-describedby'], 'account-error');
  assert.equal(view.field('password').props.autoComplete, 'new-password');
  assert.equal(view.field('confirmation').props.autoComplete, 'new-password');
  view.edit('confirmation', ' pass word ');
  await view.submit();
  assert.equal(calls.length, 1);
  assert.deepEqual(Object.keys(calls[0]).sort(), ['password', 'username']);
  assert.equal(calls[0].password, ' pass word ');
});

test('account errors stay visible and retain credentials for a retry', async () => {
  const view = fixture({ onLogin: async () => { throw new Error('Invalid username or password.'); } });
  view.edit('username', 'billy');
  view.edit('password', 'password');
  await view.submit();
  assert.equal(view.message(), 'Invalid username or password.');
  assert.equal(view.field('password').props.value, 'password');
  assert.equal(view.field('password').props.disabled, false);
});

test('checking and disabled registration cannot submit or reveal account creation', async () => {
  let calls = 0;
  const view = fixture({ checking: true, registrationAllowed: undefined, onLogin: async () => calls++ });
  view.edit('username', 'billy');
  view.edit('password', 'password');
  await view.submit();
  assert.equal(calls, 0);
  assert.equal(view.all(node => node.props.role === 'tablist').length, 0);
  assert.equal(view.field('confirmation'), undefined);
});

test('first-account setup defaults to registration before interaction and explains legacy preservation', () => {
  const view = fixture({ checking: true, registrationAllowed: false });
  view.update({ checking: false, registrationAllowed: true, legacyClaimAvailable: true });
  assert.equal(view.all(node => node.props.id === 'account-register-tab')[0].props['aria-selected'], true);
  assert.match(view.text(view.render()), /This account will keep this installation’s existing bots and files/);
  assert.match(view.text(view.render()), /Create account/);
  assert.doesNotMatch(view.text(view.render()), /Checking…/);
  const fresh = fixture({ setupRequired: true });
  assert.equal(fresh.all(node => node.props.id === 'account-register-tab')[0].props['aria-selected'], true);
  assert.doesNotMatch(fresh.text(fresh.render()), /existing bots/);
  const touched = fixture();
  touched.edit('username', 'chosen.user');
  touched.update({ legacyClaimAvailable: true });
  assert.equal(touched.all(node => node.props.id === 'account-login-tab')[0].props['aria-selected'], true, 'a later probe cannot override the user’s current form');
});

test('departing confirmation fields cannot retain focus during their animated exit', () => {
  const field = fixture({ present: false, reduced: true }).exports.LoginConfirmation({ value: '', pending: false, onChange() {} });
  assert.equal(field.props.inert, true);
  assert.equal(field.props['aria-hidden'], true);
  assert.equal(field.props.transition.duration, 0);
  assert.equal(field.props.children[1].props.disabled, true);
});
