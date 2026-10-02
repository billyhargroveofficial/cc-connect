import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../../lib/motion-stub.mjs';
import * as skillMentions from './skillMentions.ts';

const source = ts.transpileModule(readFileSync(new URL('./Composer.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

const settled = () => new Promise(resolve => setImmediate(resolve));
const transpile = path => ts.transpileModule(readFileSync(new URL(path, import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText;
const skillsSource = transpile('./useComposerSkills.ts');
const queueSource = transpile('./useMessageQueue.ts');

function deferred() {
  let resolve, reject;
  const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

// Drive the real component handlers without a browser. Only React's hook and
// JSX plumbing is stubbed; the send/upload workflow comes from Composer.tsx.
function composer({ draftScope = 'user-a', botId = 'bot', storage = new Map(), getUserMedia, catalog = [], catalogGate, offline = false, suspended = false } = {}) {
  let cursor = 0, nextUpload = 0, uploadsPaused = false;
  let effectCursor = 0;
  let present = true;
  const uploadGate = deferred();
  const values = [], requests = [], uploads = [], errors = [], catalogCalls = [];
  const recordings = [];
  const skillListeners = new Set();
  const effects = [], pendingEffects = [], traps = [];
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const element = (type, props) => ({ type, props });
  const memo = (factory, dependencies) => {
    const index = cursor++;
    const previous = values[index];
    if (!previous || dependencies.some((value, offset) => !Object.is(value, previous.dependencies[offset]))) values[index] = { dependencies, value: factory() };
    return values[index].value;
  };
  const modules = {
    react: { memo: component => component, useMemo: memo,
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
      onSkillsChanged: callback => { skillListeners.add(callback); return () => skillListeners.delete(callback); },
      api: { skills: async (id, signal, binding) => {
        catalogCalls.push({ id, signal, binding });
        return catalogGate ? catalogGate.promise : { skills: catalog };
      }, upload: async (_, file) => {
        const attachment = { id: `file-${++nextUpload}`, name: file.name, mimeType: file.type };
        uploads.push(attachment);
        if (uploadsPaused) await uploadGate.promise;
        return attachment;
      } },
      errorMessage: error => error.message,
    },
    '../../lib/motion': { ...motionTestModule(), useIsPresent: () => present },
    './ModelPicker': { default: 'ModelPicker', PresenceSurface: 'div', useDialogFocus: active => traps.push(active) },
    './SkillPicker': { default: 'SkillPicker' },
    './skillMentions': skillMentions,
    './minimal-composer.css': {},
  };
  const environment = {
    require: name => {
      assert.ok(name in modules, `Unexpected Composer import: ${name}`);
      return modules[name];
    },
    localStorage: {
      getItem: key => storage.get(key) ?? null,
      setItem: (key, value) => storage.set(key, value),
    },
    window: { isSecureContext: Boolean(getUserMedia), matchMedia: () => ({ matches: true }) },
    AbortController, requestAnimationFrame: callback => callback(),
    navigator: { mediaDevices: { getUserMedia } },
    MediaRecorder: class {
      static isTypeSupported() { return true; }
      constructor(stream) { this.stream = stream; recordings.push(this); }
      start() { this.state = 'recording'; }
      stop() { this.state = 'inactive'; this.onstop?.(); }
    },
  };
  function load(code, filename) {
    const exports = {};
    runInNewContext(code, { ...environment, exports }, { filename });
    return exports;
  }
  modules['./useMessageQueue'] = load(queueSource, 'useMessageQueue.ts');
  modules['./useComposerSkills'] = load(skillsSource, 'useComposerSkills.ts');
  const exports = load(source, 'Composer.tsx');
  const props = {
    bot: { id: botId, name: 'Bot' }, draftScope, capabilities: { voice: true }, busy: false, offline, suspended,
    context: { compacting: false, requesting: false },
    onSend: (text, attachments, skills) => {
      const request = { text, attachments, skills, ...deferred() };
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
    requests, errors, recordings, uploadedFiles: uploads, catalogCalls,
    unmount: () => { for (const effect of effects) effect?.cleanup?.(); },
    changeScope: scope => { props.draftScope = scope; render(); },
    changeBot: id => { props.bot = { ...props.bot, id }; render(); },
    availability: (backend, disabledSkills = []) => { props.bot = { ...props.bot, backend, disabledSkills }; render(); },
    catalog: next => { catalog = next; },
    catalogGate: next => { catalogGate = next; },
    skillsChanged: change => { for (const listener of skillListeners) listener(change); },
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
    modelPicker: open => find(node => node.type === 'ModelPicker').props.onOpenChange(open),
    hasContextControl: () => !!find(node => node.type === 'ContextControl'),
    pauseUploads: () => { uploadsPaused = true; },
    resumeUploads: async () => { uploadsPaused = false; uploadGate.resolve(); await settled(); },
    rejectUploads: async () => { uploadsPaused = false; uploadGate.reject(new Error('Old upload failed')); await settled(); },
    edit: (value, caret = value.length, end = caret) => find(node => node.type === 'textarea').props.onChange({ target: { value, selectionStart: caret, selectionEnd: end } }),
    focus: (caret, end = caret) => { const area = find(node => node.type === 'textarea'); area.props.onFocus?.({ currentTarget: { value: area.props.value, selectionStart: caret ?? area.props.value.length, selectionEnd: end ?? caret ?? area.props.value.length } }); },
    select: (caret, end = caret) => { const area = find(node => node.type === 'textarea'); area.props.onSelect({ currentTarget: { value: area.props.value, selectionStart: caret, selectionEnd: end } }); },
    picker: () => find(node => node.type === 'SkillPicker'),
    skillError: () => find(node => node.props?.className === 'composer-skill-error'),
    retrySkills: () => find(node => node.props?.['aria-label'] === 'Retry checking skills').props.onClick(),
    liveStatuses: () => all(node => node.props?.className?.split(' ').includes('composer-live-status')),
    attachSkill: name => { const picker = find(node => node.type === 'SkillPicker'); assert.ok(picker, 'skill list is open'); const skill = picker.props.skills.find(skill => skill.name === name); assert.ok(skill, `catalog contains ${name}`); picker.props.onChoose(skill); },
    attachSkillId: id => { const picker = find(node => node.type === 'SkillPicker'); assert.ok(picker); const skill = picker.props.skills.find(skill => skill.id === id); assert.ok(skill); picker.props.onChoose(skill); },
    skillNames: () => all(node => node.props?.className?.split(' ').includes('skill-chip')).map(chip => chip.props.children[1].props.children),
    removeSkill: name => find(node => node.props?.['aria-label'] === `Remove skill ${name}`).props.onClick(),
    key: (key, options = {}) => { let prevented = false, stopped = false; find(node => node.type === 'textarea').props.onKeyDown({ key, shiftKey: false, nativeEvent: { isComposing: false }, ...options, preventDefault() { prevented = true; }, stopPropagation() { stopped = true; } }); return { prevented, stopped }; },
    textarea: () => find(node => node.type === 'textarea'),
    send: () => find(node => node.props?.['aria-label'] === 'Send message').props.onClick(),
    sendHandler: () => find(node => node.props?.['aria-label'] === 'Send message').props.onClick,
    draft: () => find(node => node.type === 'textarea').props.value,
    uploadNames: () => all(node => node.props?.className?.split(' ').includes('upload-chip') && !node.props?.className?.split(' ').includes('skill-chip'))
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

const skill = (id, name = id, fields = {}) => ({ id, name, path: `/private/${id}/SKILL.md`, description: `Run ${name}`, scope: 'project', enabled: true, editable: true, ...fields });
const skillDraft = (...skills) => new Map([['connect-bots:skill-draft:user-a:bot', JSON.stringify(skills)]]);

test('the dollar picker lazily loads once, binds account and node, filters enabled skills, and attaches before Enter can send', async () => {
  const view = composer({ draftScope: 'account-a:mac-a', catalog: [skill('review'), skill('writer'), skill('disabled', 'review-disabled', { enabled: false })] });
  view.draft();
  assert.equal(view.catalogCalls.length, 0, 'ordinary typing does not request the skill catalog');
  view.edit('$');
  assert.ok(view.picker());
  await settled();
  assert.deepEqual(Array.from(view.picker().props.skills, entry => entry.name), ['review', 'writer']);
  assert.deepEqual(JSON.parse(JSON.stringify(view.catalogCalls[0].binding)), { accountId: 'account-a', nodeId: 'mac-a' });
  view.edit('$rev');
  assert.deepEqual(Array.from(view.picker().props.skills, entry => entry.name), ['review']);
  assert.equal(view.catalogCalls.length, 1);
  assert.equal(view.textarea().props['aria-activedescendant'], 'composer-test-option-0');
  view.busy(true);
  assert.equal(view.key('Enter').prevented, true);
  assert.equal(view.requests.length, 0, 'Enter chooses the active skill rather than sending a message');
  assert.deepEqual(view.skillNames(), ['review']);
  assert.equal(view.draft(), '');
  assert.equal(view.sendDisabled(), false, 'a selected skill can be sent on its own');
  view.send();
  assert.deepEqual(Array.from(view.requests[0].skills, entry => ({ ...entry })), [{ id: 'review', name: 'review', path: '/private/review/SKILL.md' }]);
  view.requests[0].resolve(); await settled();
  assert.deepEqual(view.skillNames(), []);
  view.unmount();
});

test('skill selection preserves suffix and caret, supports multiple distinct references, and keeps attachments', async () => {
  const view = composer({ catalog: [skill('shared', 'review'), skill('project', 'review')] });
  const carets = [];
  const area = view.textarea();
  area.props.ref.current = { style: {}, scrollHeight: 30, focus() {}, setSelectionRange: (start, end) => carets.push([start, end]) };
  await view.upload('document.txt');
  const text = 'Please $review the report.';
  view.edit(text, 10);
  view.picker(); await settled();
  view.attachSkillId('shared');
  assert.equal(view.draft(), 'Please the report.');
  assert.deepEqual(carets.at(-1), [7, 7]);
  view.edit('Please the report. $rev');
  assert.deepEqual(Array.from(view.picker().props.skills, entry => entry.id), ['project'], 'already attached IDs are excluded');
  view.attachSkillId('project');
  assert.deepEqual(view.skillNames(), ['review', 'review'], 'same names from distinct paths retain their identity');
  assert.deepEqual(view.uploadNames(), ['document.txt']);
  view.removeSkill('review');
  assert.equal(view.skillNames().length, 1);
  view.send();
  assert.equal(view.requests[0].skills.length, 1);
  assert.equal(view.requests[0].attachments[0].name, 'document.txt');
  view.requests[0].resolve(); await settled();
  view.unmount();
});

test('skill keyboard navigation, cancellation, selection movement and IME retain normal textarea behavior', async () => {
  const view = composer({ catalog: [skill('review'), skill('writer')] });
  view.edit('$'); view.picker(); await settled();
  assert.equal(view.key('ArrowUp').prevented, true);
  assert.equal(view.picker().props.index, 1, 'ArrowUp wraps to the final option');
  assert.equal(view.key('ArrowDown').prevented, true);
  assert.equal(view.picker().props.index, 0);
  assert.equal(view.key('Enter', { shiftKey: true }).prevented, false, 'Shift+Enter still inserts a newline');
  assert.equal(view.key('Enter', { nativeEvent: { isComposing: true } }).prevented, false);
  assert.equal(view.requests.length, 0, 'IME confirmation cannot submit the message');
  assert.deepEqual(view.key('Escape'), { prevented: true, stopped: true });
  assert.equal(view.picker(), undefined);
  assert.equal(view.draft(), '$');
  view.edit('$rev');
  assert.equal(view.key('Tab').prevented, false, 'Tab closes without trapping focus');
  assert.equal(view.picker(), undefined);
  view.edit('Prefix $review', 14);
  assert.ok(view.picker());
  view.select(3);
  assert.equal(view.picker(), undefined, 'moving the caret outside the mention closes suggestions');
  view.edit('$unknown');
  assert.equal(view.key('Enter').prevented, true, 'Enter cannot unexpectedly send when an empty list is open');
  assert.equal(view.requests.length, 0);
  view.unmount();
});

test('scope changes and unmount abort the catalog and isolate late results and skill drafts', async () => {
  for (const { invalidate, mounted } of [
    { invalidate: view => view.changeScope('account-b:mac-b'), mounted: true },
    { invalidate: view => view.changeBot('another-bot'), mounted: true },
    { invalidate: view => view.exit(), mounted: true },
    { invalidate: view => view.unmount(), mounted: false },
  ]) {
    const gate = deferred();
    const view = composer({ draftScope: 'account-a:mac-a', catalogGate: gate });
    view.edit('$'); view.picker();
    const first = view.catalogCalls[0];
    invalidate(view);
    assert.equal(first.signal.aborted, true);
    gate.resolve({ skills: [skill('private-a')] }); await settled();
    if (mounted) { assert.equal(view.picker(), undefined); assert.deepEqual(view.skillNames(), []); }
    view.unmount();
  }
  const storage = new Map();
  const alice = composer({ draftScope: 'account-a:mac-a', storage, catalog: [skill('private-a')] });
  alice.edit('$'); alice.picker(); await settled(); alice.attachSkill('private-a'); alice.skillNames(); alice.unmount();
  const restored = composer({ draftScope: 'account-a:mac-a', storage });
  assert.deepEqual(restored.skillNames(), ['private-a']); restored.unmount();
  const other = composer({ draftScope: 'account-a:mac-b', storage });
  assert.deepEqual(other.skillNames(), []); other.unmount();
  const bob = composer({ draftScope: 'account-b:mac-a', storage });
  assert.deepEqual(bob.skillNames(), []); bob.unmount();
});

test('skill popup closes during suspension, offline mode and other popovers without dropping selected chips', async () => {
  const view = composer({ catalog: [skill('review'), skill('writer')] });
  view.edit('$'); view.picker(); await settled(); view.attachSkill('review');
  for (const block of [() => view.suspend(true), () => view.offline(true), () => view.openActions()]) {
    view.edit('$'); assert.ok(view.picker()); block();
    assert.equal(view.picker(), undefined);
    assert.deepEqual(view.skillNames(), ['review']);
    view.suspend(false); view.offline(false);
    if (view.actionsOpen()) view.openActions();
  }
  view.edit('$'); assert.ok(view.picker()); view.modelPicker(true);
  assert.equal(view.picker(), undefined); assert.deepEqual(view.skillNames(), ['review']);
  view.unmount();
});

test('persisted skills never fetch while initially offline and validate once on reconnect even with the picker suspended', async () => {
  const gate = deferred();
  const view = composer({ storage: skillDraft(skill('review')), catalogGate: gate, offline: true, suspended: true });
  assert.deepEqual(view.skillNames(), ['review']);
  view.edit('Retained draft $review');
  assert.equal(view.picker(), undefined);
  assert.equal(view.sendDisabled(), true);
  assert.equal(view.catalogCalls.length, 0, 'persisted selections cannot request an offline host');
  assert.equal(view.skillError(), undefined);

  view.offline(false);
  assert.equal(view.catalogCalls.length, 1, 'reconnect validates retained selections without opening the picker');
  assert.equal(view.sendDisabled(), true);
  assert.equal(view.picker(), undefined);
  view.draft(); view.skillNames();
  assert.equal(view.catalogCalls.length, 1, 'renders do not restart an in-flight validation');
  gate.resolve({ skills: [skill('review', 'current-review')] }); await settled();
  assert.deepEqual(view.skillNames(), ['current-review']);
  view.suspend(false);
  assert.equal(view.sendDisabled(), false);
  assert.equal(view.catalogCalls.length, 1, 'unblocking picker interactions keeps the validated catalog');
  view.unmount();
});

test('offline aborts persisted skill validation and reconnect validates once while ignoring late success or error', async () => {
  for (const failLate of [false, true]) {
    const first = deferred(), second = deferred();
    const view = composer({ storage: skillDraft(skill('review')), catalogGate: first });
    assert.deepEqual(view.skillNames(), ['review']);
    assert.equal(view.catalogCalls.length, 1);
    view.offline(true);
    assert.equal(view.catalogCalls[0].signal.aborted, true, 'disconnect cancels validation even when suggestions are closed');
    view.edit('Edited offline'); view.skillNames();
    assert.equal(view.catalogCalls.length, 1, 'offline drafts never restart an aborted request');
    view.catalogGate(second); view.offline(false);
    assert.equal(view.catalogCalls.length, 2);
    assert.equal(view.sendDisabled(), true);
    if (failLate) first.reject(new Error('Old offline request failed'));
    else first.resolve({ skills: [] });
    await settled();
    assert.deepEqual(view.skillNames(), ['review'], 'an obsolete result cannot remove retained selections');
    assert.equal(view.skillError(), undefined, 'an obsolete failure cannot replace the fresh validation');
    assert.equal(view.catalogCalls.length, 2);
    view.edit('Edited online $'); assert.ok(view.picker());
    view.key('Escape'); view.draft();
    assert.equal(view.catalogCalls.length, 2, 'opening and closing suggestions cannot restart a retained-selection validation');
    second.resolve({ skills: [skill('review', 'reconnected-review')] }); await settled();
    assert.deepEqual(view.skillNames(), ['reconnected-review']);
    assert.equal(view.sendDisabled(), false);
    view.draft(); view.skillNames();
    assert.equal(view.catalogCalls.length, 2);
    view.unmount();
  }
});

test('reconnect discards a cached skill validation error without retrying ordinary errors in a loop', async () => {
  const first = deferred(), second = deferred();
  const view = composer({ storage: skillDraft(skill('review')), catalogGate: first });
  view.skillNames();
  first.reject(new Error('Host unavailable')); await settled();
  assert.ok(view.skillError());
  assert.equal(view.sendDisabled(), true);
  view.edit('Keep this draft'); view.draft(); view.skillNames();
  view.openActions(); view.actionsOpen(); view.openActions(); view.draft();
  await settled();
  assert.equal(view.catalogCalls.length, 1, 'typing and popover changes retain an ordinary error until retry or reconnect');

  view.offline(true);
  assert.equal(view.skillError(), undefined);
  assert.deepEqual(view.skillNames(), ['review']);
  assert.equal(view.catalogCalls.length, 1);
  view.catalogGate(second); view.offline(false);
  assert.equal(view.catalogCalls.length, 2, 'a connected host automatically retries the retained selection once');
  assert.equal(view.skillError(), undefined, 'the stale failure is cleared before the reconnect request finishes');
  assert.equal(view.sendDisabled(), true);
  second.resolve({ skills: [skill('review')] }); await settled();
  assert.deepEqual(view.skillNames(), ['review']);
  assert.equal(view.sendDisabled(), false);
  assert.equal(view.catalogCalls.length, 2);
  view.unmount();
});

test('selected skill validation errors expose a visible retry outside text-only hidden status and use space only for errors', async () => {
  const first = deferred(), second = deferred();
  const view = composer({ storage: skillDraft(skill('review')), catalogGate: first });
  assert.equal(view.skillError(), undefined, 'loading does not add a visible status row');
  first.reject(new Error('Skill host unavailable')); await settled();
  assert.equal(view.picker(), undefined, 'persisted selections validate without suggestions');
  const error = view.skillError();
  assert.ok(error, 'the closed picker still exposes the validation failure');
  assert.equal(error.props.children[0].props.children, 'Skill host unavailable');
  assert.equal(error.props.children[1].type, 'button');
  assert.equal(error.props.children[1].props['aria-label'], 'Retry checking skills');
  assert.equal(error.props.children[1].props.children, 'Retry');
  assert.equal(error.props.className.includes('composer-live-status'), false);
  assert.ok(view.liveStatuses().every(status => typeof status.props.children === 'string'), 'hidden live regions cannot contain interactive retry controls');
  const css = readFileSync(new URL('./minimal-composer.css', import.meta.url), 'utf8');
  const rowStyle = css.match(/\.composer-skill-error \{([^}]+)\}/)?.[1];
  assert.match(rowStyle, /display:\s*flex/);
  assert.doesNotMatch(rowStyle, /position:\s*absolute|clip-path|height:\s*1px/);
  assert.match(css, /\.composer-skill-error > \.text-button \{[^}]*min-height:\s*44px/);

  view.catalogGate(second); view.retrySkills();
  assert.equal(view.skillError(), undefined, 'retry removes the visible row while checking');
  assert.equal(view.catalogCalls.length, 2);
  second.resolve({ skills: [skill('review')] }); await settled();
  assert.equal(view.skillError(), undefined, 'success restores the compact composer');
  assert.equal(view.sendDisabled(), false);
  assert.equal(view.catalogCalls.length, 2);
  assert.equal(view.draft(), '');
  view.unmount();
});

test('skill catalog errors stay inside suggestions and retry without dropping the draft', async () => {
  const gate = deferred(), retry = deferred();
  const view = composer({ catalogGate: gate });
  view.edit('Keep this $review'); view.picker();
  gate.reject(new Error('Host unavailable')); await settled();
  assert.equal(view.picker().props.status, 'error');
  assert.equal(view.picker().props.error, 'Host unavailable');
  assert.deepEqual(view.errors, [], 'catalog failure does not create a workspace toast');
  assert.equal(view.draft(), 'Keep this $review');
  view.catalogGate(retry); view.picker().props.onRetry(); view.picker();
  assert.equal(view.catalogCalls.length, 2);
  retry.resolve({ skills: [skill('review')] }); await settled();
  assert.equal(view.picker().props.status, 'ready');
  assert.deepEqual(Array.from(view.picker().props.skills, entry => entry.id), ['review']);
  assert.equal(view.draft(), 'Keep this $review');
  view.unmount();
});

test('initial dollar catalog errors retry with Enter while Shift+Enter preserves normal textarea input', async () => {
  const first = deferred(), second = deferred();
  const view = composer({ catalogGate: first });
  view.edit('$'); view.picker();
  first.reject(new Error('Host unavailable')); await settled();
  assert.equal(view.picker().props.status, 'error');
  assert.deepEqual(view.skillNames(), [], 'the initial catalog has no selected skills or external retry row');
  assert.equal(view.skillError(), undefined);
  assert.equal(view.catalogCalls.length, 1);

  view.catalogGate(second);
  assert.equal(view.key('Enter', { shiftKey: true }).prevented, false, 'Shift+Enter remains available for a newline');
  assert.equal(view.picker().props.status, 'error');
  assert.equal(view.catalogCalls.length, 1, 'Shift+Enter cannot retry the catalog');
  assert.equal(view.key('Enter').prevented, true);
  view.picker();
  assert.equal(view.catalogCalls.length, 2, 'Enter retries the failed initial catalog exactly once');
  assert.equal(view.requests.length, 0, 'retry never submits the dollar mention as a message');
  second.resolve({ skills: [skill('review'), skill('writer')] }); await settled();
  assert.equal(view.picker().props.status, 'ready');
  assert.deepEqual(Array.from(view.picker().props.skills, entry => entry.id), ['review', 'writer']);
  assert.equal(view.catalogCalls.length, 2);
  assert.equal(view.draft(), '$');
  view.unmount();
});

test('initial dollar catalog errors reopen on textarea focus and retry with Enter after Tab or Shift+Tab', async () => {
  for (const shiftKey of [false, true]) {
    const first = deferred(), second = deferred();
    const view = composer({ catalogGate: first });
    view.edit('Keep this $review'); view.picker();
    first.reject(new Error('Host unavailable')); await settled();
    assert.equal(view.picker().props.status, 'error');
    assert.deepEqual(view.skillNames(), [], 'there is no selected-skill retry outside the picker');
    assert.equal(view.skillError(), undefined);

    assert.equal(view.key('Tab', { shiftKey }).prevented, false, 'Tab can move focus in either direction');
    assert.equal(view.picker(), undefined, 'leaving the textarea closes suggestions');
    view.focus();
    assert.equal(view.picker()?.props.status, 'error', 'returning focus reopens the cached error');
    assert.equal(view.catalogCalls.length, 1, 'focus alone never loops a failed catalog request');

    view.catalogGate(second);
    assert.equal(view.key('Enter').prevented, true, 'the reopened error can be retried from the keyboard');
    view.picker();
    assert.equal(view.picker().props.status, 'loading');
    assert.equal(view.catalogCalls.length, 2, 'Enter retries the failed catalog exactly once');
    assert.equal(view.requests.length, 0, 'retry cannot submit the draft');
    second.resolve({ skills: [skill('review')] }); await settled();
    assert.equal(view.picker().props.status, 'ready');
    assert.deepEqual(Array.from(view.picker().props.skills, entry => entry.id), ['review']);
    assert.equal(view.draft(), 'Keep this $review');
    view.unmount();
  }
});

test('successful skill sends clear only submitted references while edits, new selections and reattached IDs survive', async () => {
  const view = composer({ catalog: [skill('review'), skill('writer')] });
  view.edit('$'); view.picker(); await settled(); view.attachSkill('review');
  view.edit('First'); view.send();
  view.removeSkill('review');
  view.edit('Next $review'); view.attachSkill('review');
  view.edit('Next $writer'); view.attachSkill('writer');
  view.requests[0].resolve(); await settled();
  assert.equal(view.draft(), 'Next ');
  assert.deepEqual(view.skillNames(), ['review', 'writer'], 'reattaching the same ID represents a new draft selection');
  view.send(); view.requests[1].reject(new Error('Keep this draft')); await settled();
  assert.equal(view.draft(), 'Next ');
  assert.deepEqual(view.skillNames(), ['review', 'writer']);
  assert.deepEqual(view.errors, ['Keep this draft']);
  view.unmount();
});

test('same-bot backend and disabled-skill changes revalidate attachments before they can send', async () => {
  const codex = skill('codex-only'), shared = skill('shared'), project = skill('project');
  const view = composer({ draftScope: 'account:mac', catalog: [codex, shared, project] });
  view.edit('$'); view.picker(); await settled(); view.attachSkill('codex-only');
  view.edit('$'); view.attachSkill('shared');
  view.edit('$'); view.attachSkill('project');
  view.edit('Use these.');
  const oldSend = view.sendHandler();
  const gate = deferred(); view.catalogGate(gate);
  view.availability('pi');
  assert.deepEqual(view.skillNames(), ['codex-only', 'shared', 'project'], 'valid chips remain visible while effective skills load');
  assert.equal(view.sendDisabled(), true);
  oldSend(); view.send();
  assert.equal(view.requests.length, 0, 'neither a stale handler nor the current composer may send unvalidated skills');
  gate.resolve({ skills: [shared, project] }); await settled();
  assert.deepEqual(view.skillNames(), ['shared', 'project']);
  assert.equal(view.sendDisabled(), false);
  const disabled = deferred(); view.catalogGate(disabled);
  view.availability('pi', ['shared']);
  assert.equal(view.sendDisabled(), true);
  disabled.resolve({ skills: [skill('shared', 'shared', { enabled: false }), project] }); await settled();
  assert.deepEqual(view.skillNames(), ['project']);
  view.send(); assert.deepEqual(Array.from(view.requests[0].skills, reference => reference.id), ['project']);
  view.requests[0].resolve(); await settled(); view.unmount();
});

test('project and global skill mutations invalidate only the matching account, node and bot catalog', async () => {
  const view = composer({ draftScope: 'account:mac', catalog: [skill('review')] });
  view.edit('$'); view.picker(); await settled(); view.key('Escape');
  for (const change of [
    { accountId: 'another', nodeId: 'mac', botId: 'bot' },
    { accountId: 'account', nodeId: 'another', botId: 'bot' },
    { accountId: 'account', nodeId: 'mac', botId: 'another' },
  ]) {
    view.skillsChanged(change); view.edit('$'); view.picker(); await settled(); view.key('Escape');
    assert.equal(view.catalogCalls.length, 1, 'unrelated mutations do not wake or refetch this catalog');
  }
  for (const change of [{ accountId: 'account', nodeId: 'mac', botId: 'bot' }, { accountId: 'account', nodeId: 'mac' }]) {
    const before = view.catalogCalls.length;
    view.catalog([skill(`new-${before}`)]);
    view.skillsChanged(change); view.draft(); await settled();
    assert.equal(view.catalogCalls.length, before, 'an unused catalog refreshes lazily at the next dollar');
    view.edit('$'); view.picker(); await settled();
    assert.deepEqual(Array.from(view.picker().props.skills, reference => reference.id), [`new-${before}`]);
    assert.equal(view.catalogCalls.length, before + 1);
    view.key('Escape');
  }
  view.unmount();
});

test('a mutation invalidates captured send handlers immediately and reconciles canonical enabled skill metadata', async () => {
  const view = composer({ draftScope: 'account:mac', catalog: [skill('review')] });
  view.edit('$'); view.picker(); await settled(); view.attachSkill('review'); view.edit('Check this.');
  const oldSend = view.sendHandler();
  const gate = deferred(); view.catalogGate(gate);
  view.skillsChanged({ accountId: 'account', nodeId: 'mac', botId: 'bot' });
  oldSend(); assert.equal(view.requests.length, 0, 'synchronous mutation invalidation does not wait for a render');
  assert.equal(view.sendDisabled(), true);
  gate.resolve({ skills: [skill('review', 'new-review')] }); await settled();
  assert.deepEqual(view.skillNames(), ['new-review']);
  view.send(); assert.equal(view.requests[0].skills[0].name, 'new-review');
  view.requests[0].resolve(); await settled(); view.unmount();
});

test('catalog refresh during POST retains unchanged attachment object identity for exact acknowledgement', async () => {
  const view = composer({ draftScope: 'account:mac', catalog: [skill('review'), skill('writer')] });
  view.edit('$'); view.picker(); await settled(); view.attachSkill('review'); view.edit('First'); view.send();
  const submitted = view.requests[0].skills[0];
  view.edit('Next $writer'); view.attachSkill('writer');
  const gate = deferred(); view.catalogGate(gate);
  view.skillsChanged({ accountId: 'account', nodeId: 'mac' }); view.skillNames();
  gate.resolve({ skills: [skill('review'), skill('writer')] }); await settled();
  view.skillNames();
  view.requests[0].resolve(); await settled();
  assert.deepEqual(view.skillNames(), ['writer'], 'refresh does not clone submitted refs and leave stale chips after acknowledgement');
  assert.equal(submitted.id, 'review');
  assert.equal(view.draft(), 'Next ');
  view.unmount();
});

test('an obsolete catalog result cannot revive unavailable attachments after backend changes', async () => {
  const first = deferred();
  const view = composer({ draftScope: 'account:mac', catalogGate: first });
  view.edit('$'); view.picker();
  const request = view.catalogCalls[0];
  view.catalogGate(undefined); view.catalog([skill('pi-skill')]); view.availability('pi');
  assert.equal(request.signal.aborted, true);
  first.resolve({ skills: [skill('codex-only')] }); await settled();
  view.edit('$'); view.picker(); await settled();
  assert.deepEqual(Array.from(view.picker().props.skills, reference => reference.id), ['pi-skill']);
  view.unmount();
});
