import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';

const source = ts.transpileModule(readFileSync(new URL('./SidebarResizeHandle.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

// Exercise the real pointer and keyboard handlers, including their effects and
// cleanup. DOM geometry, pointer capture, storage, and React scheduling are the
// only stubs; width arithmetic and preference handling come from the component.
function resizeHarness({ viewport = 1280, stored = null, storageBlocked = false, storageGetterBlocked = false } = {}) {
  const styles = new Map(), classes = new Set(), captures = new Set(), listeners = new Map();
  const storage = new Map(stored === null ? [] : [['connect-bots:sidebar-width', String(stored)]]);
  const writes = [];
  const slots = [], pendingLayout = [], pendingEffects = [];
  let cursor = 0, dirty = false, node, mounted = true;
  const changed = (previous, next) => !previous || !next
    || previous.length !== next.length || previous.some((value, index) => !Object.is(value, next[index]));
  const state = initial => {
    const index = cursor++;
    if (!slots[index]) slots[index] = { value: typeof initial === 'function' ? initial() : initial };
    return [slots[index].value, next => {
      const value = typeof next === 'function' ? next(slots[index].value) : next;
      if (!Object.is(value, slots[index].value)) { slots[index].value = value; dirty = true; }
    }];
  };
  const effect = queue => (callback, dependencies) => {
    const index = cursor++;
    if (changed(slots[index]?.dependencies, dependencies)) {
      queue.push(() => {
        slots[index]?.cleanup?.();
        slots[index] = { dependencies, cleanup: callback() };
      });
    }
  };
  const workspace = { style: {
    setProperty: (name, value) => styles.set(name, value),
    removeProperty: name => styles.delete(name),
  } };
  const document = { documentElement: { classList: {
    add: name => classes.add(name), remove: name => classes.delete(name),
  } } };
  const window = {
    innerWidth: viewport,
    localStorage: {
      getItem(key) { if (storageBlocked) throw new Error('Storage denied'); return storage.get(key) ?? null; },
      setItem(key, value) { if (storageBlocked) throw new Error('Storage denied'); storage.set(key, value); writes.push([key, value]); },
      removeItem(key) { if (storageBlocked) throw new Error('Storage denied'); storage.delete(key); writes.push([key, null]); },
    },
    addEventListener(name, callback) {
      if (!listeners.has(name)) listeners.set(name, new Set());
      listeners.get(name).add(callback);
    },
    removeEventListener: (name, callback) => listeners.get(name)?.delete(callback),
  };
  if (storageGetterBlocked) Object.defineProperty(window, 'localStorage', {
    get() { throw new Error('Storage getter denied'); },
  });
  const target = {
    parentElement: { getBoundingClientRect: () => ({ width: window.innerWidth <= 700 ? window.innerWidth
      : styles.has('--roster-width') ? Number.parseFloat(styles.get('--roster-width'))
        : exports.defaultSidebarWidth(window.innerWidth) }) },
    closest: selector => { assert.equal(selector, '.workspace'); return workspace; },
    setPointerCapture: id => captures.add(id),
    hasPointerCapture: id => captures.has(id),
    releasePointerCapture: id => captures.delete(id),
    setAttribute(name, value) { target[name] = value; },
  };
  const modules = {
    react: {
      useState: state,
      useRef: initial => state(() => ({ current: initial }))[0],
      useCallback(callback, dependencies) {
        const index = cursor++;
        if (changed(slots[index]?.dependencies, dependencies)) slots[index] = { dependencies, callback };
        return slots[index].callback;
      },
      useLayoutEffect: effect(pendingLayout), useEffect: effect(pendingEffects),
    },
    'react/jsx-runtime': { jsx: (type, props) => ({ type, props }) },
  };
  const exports = {};
  runInNewContext(source, { exports, window, document, require: name => {
    assert.ok(name in modules, `Unexpected resize import: ${name}`); return modules[name];
  } }, { filename: 'SidebarResizeHandle.tsx' });
  const render = () => {
    assert.ok(mounted, 'cannot render an unmounted handle');
    let passes = 0;
    do {
      dirty = false; cursor = 0;
      node = exports.default();
      node.props.ref.current = target;
      for (const commit of pendingLayout.splice(0)) commit();
      for (const commit of pendingEffects.splice(0)) commit();
      assert.ok(++passes < 10, 'resize effects settle');
    } while (dirty);
    return node;
  };
  const event = fields => ({
    currentTarget: target, pointerId: 1, clientX: target.parentElement.getBoundingClientRect().width,
    button: 0, prevented: false, preventDefault() { this.prevented = true; }, ...fields,
  });
  const invoke = (name, fields = {}) => {
    const next = event(fields);
    render().props[name](next);
    render();
    return next;
  };
  const dispatch = name => {
    for (const callback of listeners.get(name) || []) callback();
    render();
  };
  render();
  return {
    exports, render, styles, classes, captures, writes, storage, listeners,
    down: fields => invoke('onPointerDown', fields), move: fields => invoke('onPointerMove', fields),
    up: fields => invoke('onPointerUp', fields), cancel: fields => invoke('onPointerCancel', fields),
    lost: fields => invoke('onLostPointerCapture', fields), key: key => invoke('onKeyDown', { key }),
    reset: () => invoke('onDoubleClick'), blur: () => dispatch('blur'),
    resize(next) { window.innerWidth = next; dispatch('resize'); },
    unmount() { mounted = false; for (const slot of slots) slot?.cleanup?.(); },
  };
}

test('sidebar bounds keep desktop conversation space and adaptive defaults', () => {
  const { exports } = resizeHarness();
  assert.equal(exports.defaultSidebarWidth(1600), 300);
  assert.equal(exports.defaultSidebarWidth(1280), 284);
  assert.equal(exports.defaultSidebarWidth(1000), 258);
  assert.equal(exports.clampSidebarWidth(80, 1280), exports.SIDEBAR_MIN_WIDTH);
  assert.equal(exports.clampSidebarWidth(900, 1280), exports.SIDEBAR_MAX_WIDTH);
  assert.equal(exports.clampSidebarWidth(420, 760), 320, 'narrow desktop leaves 440px for the conversation');
  assert.equal(exports.clampSidebarWidth(420, 701), 261);
});

test('pointer resize follows the grabbed boundary without jumping and persists only on release', () => {
  const view = resizeHarness();
  const start = view.down({ clientX: 286 });
  assert.equal(start.prevented, true);
  assert.equal(view.captures.has(1), true);
  assert.equal(view.classes.has('is-resizing-sidebar'), true);
  assert.equal(view.styles.has('--roster-width'), false, 'grabbing within the handle does not move its boundary');
  view.move({ clientX: 336 });
  assert.equal(view.styles.get('--roster-width'), '334px');
  assert.equal(view.styles.get('--roster-half-width'), '167px');
  assert.equal(view.writes.length, 0, 'streaming pointer positions do not write storage');
  view.up({ clientX: 336 });
  assert.equal(view.classes.has('is-resizing-sidebar'), false);
  assert.equal(view.captures.size, 0);
  assert.equal(view.render().props['aria-valuenow'], 334);
  assert.deepEqual(view.writes, [[view.exports.SIDEBAR_STORAGE_KEY, '334']]);
});

test('resize clamps pointer moves and ignores right click or another pointer', () => {
  const view = resizeHarness();
  assert.equal(view.down({ button: 2 }).prevented, false);
  assert.equal(view.down({ isPrimary: false }).prevented, false);
  assert.equal(view.classes.size, 0);
  view.down({ clientX: 284 });
  assert.equal(view.down({ pointerId: 2, clientX: 350 }).prevented, false, 'a second pointer cannot replace the captured drag');
  view.move({ pointerId: 2, clientX: 400 });
  view.up({ pointerId: 2 });
  view.lost({ pointerId: 2 });
  assert.equal(view.classes.has('is-resizing-sidebar'), true, 'a different pointer cannot end this drag');
  assert.equal(view.styles.has('--roster-width'), false);
  view.move({ clientX: -1000 });
  assert.equal(view.styles.get('--roster-width'), '220px');
  view.move({ clientX: 3000 });
  assert.equal(view.styles.get('--roster-width'), '420px');
  view.up();
  assert.equal(view.storage.get(view.exports.SIDEBAR_STORAGE_KEY), '420');
});

test('keyboard separator resizes, exposes its bounds, and double click restores the adaptive default', () => {
  const view = resizeHarness();
  const separator = view.render();
  assert.equal(separator.props.role, 'separator');
  assert.equal(separator.props['aria-orientation'], 'vertical');
  assert.equal(separator.props['aria-label'], 'Resize bot sidebar');
  assert.equal(separator.props.tabIndex, 0);
  assert.equal(view.key('ArrowRight').prevented, true);
  assert.equal(view.styles.get('--roster-width'), '300px');
  view.key('ArrowLeft');
  assert.equal(view.styles.get('--roster-width'), '284px');
  view.key('Home');
  assert.equal(view.render().props['aria-valuenow'], 220);
  view.key('End');
  assert.equal(view.render().props['aria-valuenow'], 420);
  assert.equal(view.key('Tab').prevented, false, 'unrelated keyboard navigation stays native');
  view.reset();
  assert.equal(view.styles.has('--roster-width'), false);
  assert.equal(view.styles.has('--roster-half-width'), false);
  assert.equal(view.storage.has(view.exports.SIDEBAR_STORAGE_KEY), false);
  assert.equal(view.render().props['aria-valuenow'], 284);
});

test('saved width survives temporary narrow or mobile viewports without constraining mobile layout', () => {
  const view = resizeHarness({ stored: 410 });
  assert.equal(view.styles.get('--roster-width'), '410px');
  view.resize(760);
  assert.equal(view.styles.get('--roster-width'), '320px');
  assert.equal(view.storage.get(view.exports.SIDEBAR_STORAGE_KEY), '410', 'viewport clamping preserves the chosen desktop width');
  view.resize(390);
  assert.equal(view.styles.has('--roster-width'), false);
  assert.equal(view.styles.has('--roster-half-width'), false);
  assert.equal(view.down().prevented, false);
  assert.equal(view.key('ArrowRight').prevented, false);
  assert.equal(view.writes.length, 0);
  view.resize(1280);
  assert.equal(view.styles.get('--roster-width'), '410px');
  assert.equal(view.render().props['aria-valuenow'], 410);
});

test('separator bounds update when the window changes even if its saved width remains the same', () => {
  const view = resizeHarness({ stored: 220 });
  assert.equal(view.render().props['aria-valuemax'], 420);
  view.resize(701);
  assert.equal(view.render().props['aria-valuenow'], 220);
  assert.equal(view.render().props['aria-valuemax'], 261);
  view.resize(1280);
  assert.equal(view.render().props['aria-valuemax'], 420);
});

test('cancellation, lost capture, blur, mobile transition, and unmount clear resize mode', () => {
  for (const finish of ['cancel', 'lost', 'blur', 'mobile', 'unmount']) {
    const view = resizeHarness();
    view.down({ clientX: 284 });
    view.move({ clientX: 354 });
    if (finish === 'mobile') view.resize(390);
    else view[finish]();
    assert.equal(view.classes.has('is-resizing-sidebar'), false, `${finish} restores selection and cursor`);
    if (finish !== 'unmount') {
      assert.equal(view.storage.get(view.exports.SIDEBAR_STORAGE_KEY), '354', `${finish} preserves the last displayed width`);
      view.unmount();
    }
    assert.equal(view.listeners.get('resize').size, 0);
    assert.equal(view.listeners.get('blur').size, 0);
  }
});

test('invalid or denied local storage does not prevent resizing', () => {
  for (const stored of ['invalid', 100, 999, Infinity]) {
    const view = resizeHarness({ stored });
    assert.equal(view.render().props['aria-valuenow'], 284);
    assert.equal(view.styles.has('--roster-width'), false);
  }
  for (const options of [{ storageBlocked: true }, { storageGetterBlocked: true }]) {
    const view = resizeHarness(options);
    assert.doesNotThrow(() => {
      view.down({ clientX: 284 }); view.move({ clientX: 344 }); view.up();
      view.key('End'); view.reset();
    });
    assert.equal(view.classes.size, 0);
    assert.equal(view.render().props['aria-valuenow'], 284);
  }
});
