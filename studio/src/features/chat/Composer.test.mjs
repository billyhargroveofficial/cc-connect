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
function composer({ draftScope = 'user-a', botId = 'bot', storage = new Map(), getUserMedia } = {}) {
  let cursor = 0, nextUpload = 0, uploadsPaused = false;
  let effectCursor = 0;
  let present = true;
  const uploadGate = deferred();
  const values = [], requests = [], uploads = [], errors = [];
  const recordings = [];
  const effects = [], pendingEffects = [], traps = [];
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const element = (type, props) => ({ type, props });
  const modules = {
    react: { memo: component => component, useMemo: factory => factory(),
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
    '../../lib/motion': { ...motionTestModule(), useIsPresent: () => present },
    './ModelPicker': { default: 'ModelPicker', PresenceSurface: 'div', useDialogFocus: active => traps.push(active) },
    './minimal-composer.css': {},
  };
  const exports = {};
  runInNewContext(source, {
    exports,
    require: name => {
      assert.ok(name in modules, `Unexpected Composer import: ${name}`);
      return modules[name];
    },
    localStorage: {
      getItem: key => storage.get(key) ?? null,
      setItem: (key, value) => storage.set(key, value),
    },
    window: { isSecureContext: Boolean(getUserMedia) },
    navigator: { mediaDevices: { getUserMedia } },
    MediaRecorder: class {
      static isTypeSupported() { return true; }
      constructor(stream) { this.stream = stream; recordings.push(this); }
      start() { this.state = 'recording'; }
      stop() { this.state = 'inactive'; this.onstop?.(); }
    },
  }, { filename: 'Composer.tsx' });
  const props = {
    bot: { id: botId, name: 'Bot' }, draftScope, capabilities: { voice: true }, busy: false,
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
    requests, errors, recordings, uploadedFiles: uploads,
    unmount: () => { for (const effect of effects) effect?.cleanup?.(); },
    changeScope: scope => { props.draftScope = scope; render(); },
    exit: () => { present = false; render(); },
    microphone: () => find(node => node.props?.['aria-label'] === 'Dictation').props.onClick(),
    suspend: suspended => { props.suspended = suspended; render(); },
    offline: offline => { props.offline = offline; render(); },
    busy: busy => { props.busy = busy; render(); },
    sendDisabled: () => find(node => node.props?.['aria-label'] === 'Send message').props.disabled,
    openActions: () => find(node => node.props?.['aria-label'] === 'Attach file').props.onClick(),
    actionsOpen: () => !!find(node => node.props?.className === 'composer-actions-menu'),
    trapActive: () => { render(); return traps.at(-1); },
    controlsSuspended: () => find(node => node.type === 'ModelPicker').props.disabled,
    hasContextControl: () => !!find(node => node.type === 'ContextControl'),
    pauseUploads: () => { uploadsPaused = true; },
    resumeUploads: async () => { uploadsPaused = false; uploadGate.resolve(); await settled(); },
    rejectUploads: async () => { uploadsPaused = false; uploadGate.reject(new Error('Old upload failed')); await settled(); },
    edit: value => find(node => node.type === 'textarea').props.onChange({ target: { value } }),
    send: () => find(node => node.props?.['aria-label'] === 'Send message').props.onClick(),
    draft: () => find(node => node.type === 'textarea').props.value,
    uploadNames: () => all(node => node.props?.className?.split(' ').includes('upload-chip'))
      .map(chip => chip.props.children[1].props.children),
    upload: async names => {
      find(node => node.type === 'input' && node.props.multiple).props.onChange({
        target: { files: (Array.isArray(names) ? names : [names]).map(name => ({ name, type: 'text/plain' })) },
      });
      await settled();
      return uploads.at(-1);
    },
};
}

test('queued uploads cannot continue after another account or host, animated exit, or unmount', async () => {
  for (const failed of [false, true]) {
    for (const invalidate of [
      view => view.changeScope('user-b:local'),
      view => view.changeScope('user-a:mac'),
      view => view.exit(),
      view => view.unmount(),
    ]) {
      const view = composer({ draftScope: 'user-a:local' });
      view.pauseUploads();
      await view.upload(['first.txt', 'queued-private.txt']);
      assert.deepEqual(view.uploadedFiles.map(file => file.name), ['first.txt']);
      invalidate(view);
      await (failed ? view.rejectUploads() : view.resumeUploads());
      assert.deepEqual(view.uploadedFiles.map(file => file.name), ['first.txt'],
        'the old queue must not start a request with the newly active API identity');
      assert.deepEqual(view.errors, []);
      view.unmount();
    }
  }
});

test('microphone permission granted after sign-out stops every track without creating a recorder', async () => {
  const permission = deferred();
  let stopped = 0;
  const stream = { getTracks: () => [{ stop: () => stopped++ }, { stop: () => stopped++ }] };
  const view = composer({ getUserMedia: () => permission.promise });
  view.microphone();
  view.unmount();
  permission.resolve(stream);
  await settled();
  assert.equal(stopped, 2, 'late permission must immediately release every captured track');
  assert.equal(view.recordings.length, 0, 'an unmounted composer cannot create a hidden recorder');
  assert.deepEqual(view.errors, []);
});

test('microphone permission from a previous account or exiting composer cannot start recording', async () => {
  for (const invalidate of [view => view.changeScope('user-b'), view => view.exit()]) {
    const permission = deferred();
    let stopped = 0;
    const view = composer({ getUserMedia: () => permission.promise });
    view.microphone();
    invalidate(view);
    permission.resolve({ getTracks: () => [{ stop: () => stopped++ }] });
    await settled();
    assert.equal(stopped, 1);
    assert.equal(view.recordings.length, 0);
    view.unmount();
  }
});

test('microphone permission for the current composer starts recording and releases capture on unmount', async () => {
  const permission = deferred();
  let stopped = 0;
  const stream = { getTracks: () => [{ stop: () => stopped++ }] };
  const view = composer({ getUserMedia: () => permission.promise });
  view.microphone();
  permission.resolve(stream);
  await settled();
  assert.equal(view.recordings.length, 1);
  assert.equal(view.recordings[0].state, 'recording');
  assert.equal(stopped, 0);
  view.unmount();
  assert.equal(view.recordings[0].state, 'inactive');
  assert.equal(stopped, 1);
});

test('composer drafts persist only for the same account and bot', () => {
  const storage = new Map([['connect-bots:draft:bot', 'Legacy private draft']]);
  const alice = composer({ draftScope: 'user-alice', storage });
  assert.equal(alice.draft(), '', 'unscoped legacy drafts are never loaded by an account');
  alice.edit('Alice private draft');
  assert.equal(alice.draft(), 'Alice private draft');

  const bob = composer({ draftScope: 'user-bob', storage });
  assert.equal(bob.draft(), '', 'another account cannot load the draft for the same bot id');
  bob.edit('Bob private draft');
  assert.equal(bob.draft(), 'Bob private draft');

  assert.equal(composer({ draftScope: 'user-alice', storage }).draft(), 'Alice private draft');
  assert.equal(composer({ draftScope: 'user-bob', storage }).draft(), 'Bob private draft');
  assert.equal(composer({ draftScope: 'user-alice', botId: 'another-bot', storage }).draft(), '');
});

test('composer without an account identity cannot read or persist a draft', () => {
  const storage = new Map([['connect-bots:draft:bot', 'Legacy private draft']]);
  const anonymous = composer({ draftScope: '', storage });
  assert.equal(anonymous.draft(), '');
  anonymous.edit('Not stored');
  assert.equal(anonymous.draft(), 'Not stored');
  assert.deepEqual(Array.from(storage), [['connect-bots:draft:bot', 'Legacy private draft']]);
});

test('an offline host blocks sending and uploading while preserving the editable draft and files', async () => {
  const view = composer();
  view.edit('Keep this draft on reconnect');
  await view.upload('ready.txt');
  view.offline(true);
  assert.equal(view.sendDisabled(), true);
  view.send();
  assert.equal(view.requests.length, 0);
  await view.upload('offline.txt');
  assert.deepEqual(view.uploadNames(), ['ready.txt']);
  view.edit('Edited while offline');
  assert.equal(view.draft(), 'Edited while offline');
  assert.equal(view.controlsSuspended(), true);
  view.offline(false);
  assert.equal(view.sendDisabled(), false);
  view.send();
  assert.equal(view.requests[0].text, 'Edited while offline');
  assert.deepEqual(Array.from(view.requests[0].attachments, file => file.name), ['ready.txt']);
  view.requests[0].resolve();
  await settled();
  view.unmount();
});

test('an upload acknowledgement during a temporary host disconnect settles the retained attachment', async () => {
  const view = composer({ draftScope: 'user-a:mac' });
  view.pauseUploads();
  await view.upload('accepted-before-disconnect.txt');
  view.offline(true);
  await view.resumeUploads();
  view.offline(false);
  assert.equal(view.sendDisabled(), false, 'the retained attachment must not remain stuck as uploading after reconnect');
  view.send();
  assert.deepEqual(Array.from(view.requests[0].attachments, file => file.name), ['accepted-before-disconnect.txt']);
  view.requests[0].resolve();
  await settled();
  view.unmount();
});

test('microphone permission that arrives after the host disconnects releases capture', async () => {
  const permission = deferred();
  let stopped = 0;
  const view = composer({ getUserMedia: () => permission.promise });
  view.microphone();
  view.offline(true);
  permission.resolve({ getTracks: () => [{ stop: () => stopped++ }] });
  await settled();
  assert.equal(stopped, 1);
  assert.equal(view.recordings.length, 0);
  view.unmount();
});

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
  assert.equal(view.controlsSuspended(), true);
  assert.equal(view.draft(), 'Keep this mobile draft');
  assert.deepEqual(view.uploadNames(), ['draft.txt']);
  view.send();
  assert.equal(view.requests.length, 0, 'a hidden conversation cannot submit its retained draft');

  view.suspend(false);
  assert.equal(view.trapActive(), false);
  assert.equal(view.actionsOpen(), false, 'the dismissed popup does not reopen on return');
  assert.equal(view.controlsSuspended(), false);
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


test('busy composer submits the next message instead of stopping and preserves a newer draft on acknowledgement', async () => {
  const view = composer();
  view.busy(true);
  view.edit('First queued instruction');
  await view.upload('queued.txt');
  assert.equal(view.sendDisabled(), false, 'an active turn permits a queued message');
  assert.equal(view.hasContextControl(), false, 'context moved to the statusline');
  view.send();
  assert.equal(view.requests[0].text, 'First queued instruction');
  view.edit('Another queued instruction');
  await view.upload('next.txt');
  view.requests[0].resolve();
  await settled();
  assert.equal(view.draft(), 'Another queued instruction');
  assert.deepEqual(view.uploadNames(), ['next.txt']);
  view.unmount();
});
