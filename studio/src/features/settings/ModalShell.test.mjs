import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../../lib/motion-stub.mjs';

const source = ts.transpileModule(readFileSync(new URL('./shared.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function modalHarness() {
  const document = { body: { style: { overflow: '' } }, activeElement: null };
  const listeners = new Map();
  const frames = new Map(), dialogs = [];
  let nextFrame = 0, cursor = 0, present = true;
  const refs = [];
  document.addEventListener = (name, callback) => listeners.set(name, callback);
  document.removeEventListener = name => listeners.delete(name);
  class Element {
    constructor({ inert = false, tabIndex = 0 } = {}) { this.inert = inert; this.tabIndex = tabIndex; this.isConnected = true; }
    closest() { return this.inert ? {} : null; }
    getClientRects() { return [{}]; }
    focus() { document.activeElement = this; }
    contains(focused) { return focused === this; }
  }
  document.querySelectorAll = () => dialogs;
  const effects = [], element = (type, props) => ({ type, props });
  const modules = {
    react: { useRef: current => refs[cursor++] ?? (refs[cursor - 1] = { current }), useEffect: callback => effects.push(callback) },
    'react-dom': { createPortal: children => children },
    'react/jsx-runtime': { jsx: element, jsxs: element },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    'react-markdown': { default: 'ReactMarkdown' },
    'remark-gfm': { default: 'remarkGfm' },
    '../../lib/types': {},
    '../../lib/avatars': { avatarStyles: [] },
    '../../components/Avatar': { default: 'Avatar' },
    '../../lib/motion': { ...motionTestModule(), useIsPresent: () => present },
    './settings.css': {},
  };
  const exports = {};
  runInNewContext(source, {
    exports, document, HTMLElement: Element, require: name => modules[name],
    window: {
      requestAnimationFrame: callback => { const id = ++nextFrame; frames.set(id, callback); return id; },
      cancelAnimationFrame: id => frames.delete(id),
    },
  }, { filename: 'shared.tsx' });
  const first = new Element(), last = new Element();
  const closingSave = new Element({ inert: true });
  const notTabbable = new Element({ tabIndex: -1 });
  const panel = new Element({ tabIndex: -1 });
  panel.controls = [first, last, closingSave, notTabbable];
  panel.querySelectorAll = () => panel.controls;
  panel.contains = focused => focused === panel || panel.controls.includes(focused);
  dialogs.push(panel);
  return {
    document, dialogs, Element, panel, first, last,
    render(nextPresent = true) {
      cursor = 0; effects.length = 0; present = nextPresent;
      const overlay = exports.ModalShell({ title: 'Shared settings', onClose() {}, children: null });
      overlay.props.children.props.ref.current = panel;
      return overlay;
    },
    mountEffect: () => effects[0](),
    keyboard: event => listeners.get('keydown')(event),
    focusin: event => listeners.get('focusin')?.(event),
    flushFrames() {
      const pending = [...frames.values()]; frames.clear();
      pending.forEach(callback => callback());
    },
  };
}

test('settings modal Tab boundary ignores inert closing panes and controls outside the tab order', () => {
  const view = modalHarness();
  view.render();
  const cleanup = view.mountEffect();
  let prevented = 0;
  view.document.activeElement = view.last;
  view.keyboard({ key: 'Tab', shiftKey: false, preventDefault() { prevented += 1; } });
  assert.equal(view.document.activeElement, view.first, 'forward Tab wraps at the last live control during the pane exit');
  view.keyboard({ key: 'Tab', shiftKey: true, preventDefault() { prevented += 1; } });
  assert.equal(view.document.activeElement, view.last, 'reverse Tab returns to the last live control, never to an inert Save button');
  assert.equal(prevented, 2);
  cleanup();
  view.flushFrames();
  assert.equal(view.document.body.style.overflow, '');
});

test('settings modal retains keyboard focus when a dynamic pane removes the focused control', () => {
  const view = modalHarness();
  view.render();
  const cleanup = view.mountEffect();
  view.last.focus();
  view.panel.controls = [view.first, new view.Element()];
  // Removing a focused form submit (for example after host enrollment)
  // resets the browser's activeElement to BODY without firing focusin.
  view.document.activeElement = view.document.body;
  let prevented = 0;
  view.keyboard({ key: 'Tab', shiftKey: false, preventDefault() { prevented += 1; } });
  assert.equal(view.document.activeElement, view.first);
  view.document.activeElement = view.document.body;
  view.keyboard({ key: 'Tab', shiftKey: true, preventDefault() { prevented += 1; } });
  assert.equal(view.document.activeElement, view.panel.controls.at(-1));
  assert.equal(prevented, 2);
  const background = new view.Element();
  background.focus();
  view.focusin({ target: background });
  assert.equal(view.document.activeElement, view.first, 'programmatic focus cannot escape the active modal');
  cleanup();
  background.focus();
  view.focusin({ target: background });
  assert.equal(view.document.activeElement, background, 'closing releases focus containment');
  view.flushFrames();
});

test('settings modal captures its trigger before mount focus and restores it after inert clears focus to BODY', () => {
  const view = modalHarness();
  const trigger = new view.Element();
  trigger.focus();
  view.render();
  // A descendant may autofocus before the shell's effect, so return focus
  // must come from the first render rather than the effect's activeElement.
  view.first.focus();
  const cleanup = view.mountEffect();
  assert.equal(view.document.activeElement, view.panel);
  view.panel.inert = true;
  view.document.activeElement = view.document.body;
  cleanup();
  view.render(false);
  assert.equal(view.document.activeElement, view.document.body, 'restoration waits until the inert commit finishes');
  view.flushFrames();
  assert.equal(view.document.activeElement, trigger);
});

test('Strict Mode effect replay cancels early modal focus restoration', () => {
  const view = modalHarness();
  const trigger = new view.Element();
  trigger.focus();
  view.render();
  view.mountEffect()();
  const cleanup = view.mountEffect();
  view.flushFrames();
  assert.equal(view.document.activeElement, view.panel, 'Strict Mode cleanup must not move focus back while the modal is open');
  view.panel.inert = true;
  view.document.activeElement = view.document.body;
  cleanup();
  view.flushFrames();
  assert.equal(view.document.activeElement, trigger);
});

test('closing settings does not steal focus from a newly opened active modal', () => {
  const view = modalHarness();
  const trigger = new view.Element();
  trigger.focus();
  view.render();
  const cleanup = view.mountEffect();
  view.panel.inert = true;
  view.document.activeElement = view.document.body;
  cleanup();
  const nextModal = new view.Element();
  view.dialogs.push(nextModal);
  view.flushFrames();
  assert.equal(view.document.activeElement, view.document.body, 'the new active modal owns the next focus move');
});
