import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../../lib/motion-stub.mjs';

const source = ts.transpileModule(readFileSync(new URL('./Composer.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

const settled = () => new Promise(resolve => setImmediate(resolve));

function deferred() {
  let resolve, reject;
  const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

// Drive the real component handlers without a browser. Only React's hook and
// JSX plumbing is stubbed; the send/upload workflow comes from Composer.tsx.
function composer() {
  let cursor = 0, nextUpload = 0, uploadsPaused = false;
  let effectCursor = 0;
  const uploadGate = deferred();
  const values = [], requests = [], uploads = [], errors = [];
  const effects = [], pendingEffects = [], traps = [];
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
      useId: () => state('composer-test')[0],
      useEffect(callback, dependencies) {
        const index = effectCursor++;
        const previous = effects[index];
        if (!previous || !dependencies || dependencies.some((value, offset) => !Object.is(value, previous.dependencies?.[offset]))) {
          pendingEffects.push(() => {
            previous?.cleanup?.();
            effects[index] = { dependencies, cleanup: callback() };
          });
        }
      },
      createElement: (type, props, ...children) => element(type, { ...props, children }),
    },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../../lib/api': {
      api: { upload: async (_, file) => {
        const attachment = { id: `file-${++nextUpload}`, name: file.name, mimeType: file.type };
        uploads.push(attachment);
        if (uploadsPaused) await uploadGate.promise;
        return attachment;
      } },
      errorMessage: error => error.message,
    },
    '../../lib/motion': motionTestModule(),
    './ModelPicker': { default: 'ModelPicker', PresenceSurface: 'div', useDialogFocus: active => traps.push(active) },
    './ContextControl': { default: 'ContextControl' },
    './minimal-composer.css': {},
  };
  const exports = {};
  runInNewContext(source, {
    exports,
    require: name => {
      assert.ok(name in modules, `Unexpected Composer import: ${name}`);
      return modules[name];
    },
    localStorage: { getItem: () => null },
    window: {},
  }, { filename: 'Composer.tsx' });
  const props = {
    bot: { id: 'bot', name: 'Bot' }, capabilities: null, busy: false,
    context: { compacting: false, requesting: false },
    onSend: (text, attachments) => {
      const request = { text, attachments, ...deferred() };
      requests.push(request);
      return request.promise;
    },
    onStop: async () => {}, onBotChange() {}, onError: error => errors.push(error),
  };
  const render = () => {
    cursor = 0; effectCursor = 0;
    const tree = exports.default(props);
    for (const effect of pendingEffects.splice(0)) effect();
    return tree;
  };
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
    requests, errors,
    suspend: suspended => { props.suspended = suspended; render(); },
    openActions: () => find(node => node.props?.['aria-label'] === 'More actions').props.onClick(),
    actionsOpen: () => !!find(node => node.props?.className === 'composer-actions-menu'),
    trapActive: () => { render(); return traps.at(-1); },
    contextSuspended: () => find(node => node.type === 'ContextControl').props.suspended,
    pauseUploads: () => { uploadsPaused = true; },
    resumeUploads: async () => { uploadsPaused = false; uploadGate.resolve(); await settled(); },
    edit: value => find(node => node.type === 'textarea').props.onChange({ target: { value } }),
    send: () => find(node => node.props?.['aria-label'] === 'Send message').props.onClick(),
    draft: () => find(node => node.type === 'textarea').props.value,
    uploadNames: () => all(node => node.props?.className?.split(' ').includes('upload-chip'))
      .map(chip => chip.props.children[1].props.children),
    upload: async name => {
      find(node => node.type === 'input' && node.props.multiple).props.onChange({
        target: { files: [{ name, type: 'text/plain' }] },
      });
      await settled();
      return uploads.at(-1);
    },
  };
}

test('suspending a retained mobile composer releases popup focus and keeps draft files', async () => {
  const view = composer();
  view.edit('Keep this mobile draft');
  const attachment = await view.upload('draft.txt');
  view.openActions();
  assert.equal(view.actionsOpen(), true);
  assert.equal(view.trapActive(), true);

  view.suspend(true);
  assert.equal(view.trapActive(), false);
  assert.equal(view.actionsOpen(), false);
  assert.equal(view.contextSuspended(), true);
  assert.equal(view.draft(), 'Keep this mobile draft');
  assert.deepEqual(view.uploadNames(), ['draft.txt']);
  view.send();
  assert.equal(view.requests.length, 0, 'a hidden conversation cannot submit its retained draft');

  view.suspend(false);
  assert.equal(view.trapActive(), false);
  assert.equal(view.actionsOpen(), false, 'the dismissed popup does not reopen on return');
  assert.equal(view.contextSuspended(), false);
  view.send();
  assert.equal(view.requests[0].text, 'Keep this mobile draft');
  assert.deepEqual(Array.from(view.requests[0].attachments, file => file.id), [attachment.id]);
  view.requests[0].resolve();
  await view.requests[0].promise;
  await settled();
});

test('message acknowledgement preserves draft edits and files added during POST', async () => {
  const view = composer();
  view.edit('  First message  ');
  const firstFile = await view.upload('first.txt');
  view.send();
  assert.equal(view.requests[0].text, 'First message');
  assert.deepEqual(Array.from(view.requests[0].attachments, file => file.id), [firstFile.id]);

  view.edit('Next message');
  const nextFile = await view.upload('next.txt');
  view.requests[0].resolve();
  await view.requests[0].promise;
  await settled();
  assert.equal(view.draft(), 'Next message');
  assert.deepEqual(view.uploadNames(), ['next.txt']);

  view.send();
  assert.equal(view.requests[1].text, 'Next message');
  assert.deepEqual(Array.from(view.requests[1].attachments, file => file.id), [nextFile.id]);
  view.requests[1].resolve();
  await view.requests[1].promise;
  await settled();
  assert.equal(view.draft(), '');
  assert.deepEqual(view.uploadNames(), []);
});

test('unchanged submitted text clears while a later attachment stays in the draft', async () => {
  const view = composer();
  view.edit('First message');
  await view.upload('first.txt');
  view.send();
  await view.upload('later.txt');
  view.requests[0].resolve();
  await view.requests[0].promise;
  await settled();
  assert.equal(view.draft(), '');
  assert.deepEqual(view.uploadNames(), ['later.txt']);
});

test('editing and restoring the submitted text during POST keeps the new draft', async () => {
  const view = composer();
  view.edit('Repeated instruction');
  view.send();
  view.edit('Another instruction');
  view.edit('Repeated instruction');
  view.requests[0].resolve();
  await view.requests[0].promise;
  await settled();
  assert.equal(view.draft(), 'Repeated instruction');
});

test('an attachment still uploading when POST completes remains available for the next message', async () => {
  const view = composer();
  view.edit('First message');
  await view.upload('first.txt');
  view.send();
  view.pauseUploads();
  const nextFile = await view.upload('still-uploading.txt');
  view.requests[0].resolve();
  await view.requests[0].promise;
  await settled();
  assert.equal(view.draft(), '');
  assert.deepEqual(view.uploadNames(), ['still-uploading.txt']);
  view.send();
  assert.equal(view.requests.length, 1, 'the pending upload still blocks submission');

  await view.resumeUploads();
  view.send();
  assert.equal(view.requests[1].text, '');
  assert.deepEqual(Array.from(view.requests[1].attachments, file => file.id), [nextFile.id]);
  view.requests[1].resolve();
  await view.requests[1].promise;
  await settled();
  assert.deepEqual(view.uploadNames(), []);
});

test('a failed POST retains the draft and attachments including changes made in flight', async () => {
  const view = composer();
  view.edit('First message');
  await view.upload('first.txt');
  view.send();
  view.edit('Revised message');
  await view.upload('later.txt');
  view.requests[0].reject(new Error('Offline'));
  await view.requests[0].promise.catch(() => {});
  await settled();
  assert.equal(view.draft(), 'Revised message');
  assert.deepEqual(view.uploadNames(), ['first.txt', 'later.txt']);
  assert.deepEqual(view.errors, ['Offline']);
});
