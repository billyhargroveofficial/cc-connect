import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import * as reducer from './reducer.ts';
import { motionTestModule } from '../../lib/motion-stub.mjs';

const source = ts.transpileModule(
  `${readFileSync(new URL('./Transcript.tsx', import.meta.url), 'utf8')}\nexport { ActivityItem, TurnActivity, Attachments, Message, RawDetails, JournalItem, RawJournal, RequestCard, Goal };`,
  { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX } },
).outputText;

// Exercise the disclosure's actual handlers and hooks, including the exit
// timer. Rendering and markdown internals are outside this state test.
function disclosure(component, initialProps, motionOverrides = {}) {
  let cursor = 0, nextTimer = 0;
  const values = [], effects = [], pendingEffects = [], timers = new Map();
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], value => { values[index] = typeof value === 'function' ? value(values[index]) : value; }];
  };
  const element = (type, props) => ({ type, props });
  const modules = {
    react: {
      useState: state,
      useId: () => state(`disclosure-${cursor}`)[0],
      useMemo: callback => callback(),
      useEffect: (callback, dependencies) => {
        const index = cursor++;
        const previous = effects[index];
        if (previous && dependencies.every((value, i) => Object.is(value, previous.dependencies[i]))) return;
        pendingEffects.push(() => {
          previous?.cleanup?.();
          effects[index] = { dependencies, cleanup: callback() };
        });
      },
    },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    'react-markdown': {}, 'remark-gfm': {}, 'remark-math': {}, 'rehype-highlight': {}, 'rehype-katex': {},
    '../../components/Avatar': {}, '../../lib/events': { botMessagePresentation: () => null },
    '../../lib/motion': { ...motionTestModule(), ...motionOverrides },
    './reducer': reducer, './transcript.css': {},
  };
  const exports = {};
  runInNewContext(source, {
    exports,
    require: name => {
      assert.ok(name in modules, `Unexpected transcript import: ${name}`);
      return modules[name];
    },
    window: {
      setTimeout: (callback, delay) => { timers.set(++nextTimer, { callback, delay }); return nextTimer; },
      clearTimeout: id => timers.delete(id),
    },
  }, { filename: 'Transcript.tsx' });
  let props = initialProps;
  function render() {
    cursor = 0;
    const node = exports[component](props);
    pendingEffects.splice(0).forEach(effect => effect());
    return node;
  }
  function find(match) {
    function visit(node) {
      if (!node || typeof node !== 'object') return;
      if (match(node)) return node;
      const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children];
      for (const child of children.flat(Infinity)) {
        const result = visit(child);
        if (result) return result;
      }
    }
    return visit(render());
  }
  function findAll(match) {
    const matches = [];
    function visit(node) {
      if (!node || typeof node !== 'object') return;
      if (match(node)) matches.push(node);
      const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children];
      children.flat(Infinity).forEach(visit);
    }
    visit(render());
    return matches;
  }
  return {
    update: next => { props = { ...props, ...next }; render(); },
    find,
    findAll,
    toggle: () => find(node => node.type === 'button' && node.props['aria-controls']).props.onClick(),
    finishExit: () => {
      const callbacks = [...timers.values()]; timers.clear();
      callbacks.forEach(({ callback }) => callback());
    },
    timerDelays: () => [...timers.values()].map(({ delay }) => delay),
    panel: () => find(node => node.props?.className?.startsWith('transcript-disclosure') && node.props.id),
  };
}

const activity = {
  id: 'tool-1', kind: 'tool', title: 'exec_command', status: 'inProgress',
  text: '', input: { cmd: 'pwd' }, output: '', data: { type: 'commandExecution' },
};

test('tool disclosure becomes inaccessible while its exit animation retains the payload', () => {
  const view = disclosure('ActivityItem', { activity, onPermission() {} });
  assert.equal(view.panel().props['aria-hidden'], true);
  assert.equal(view.find(node => node.props?.className === 'transcript-activity-content'), undefined);

  view.toggle();
  assert.equal(view.panel().props['aria-hidden'], false);
  assert.equal(view.panel().props.inert, false);
  assert.ok(view.find(node => node.props?.className === 'transcript-activity-content'));

  view.toggle();
  assert.equal(view.panel().props['aria-hidden'], true);
  assert.equal(view.panel().props.inert, true, 'closing content must not remain keyboard reachable');
  assert.ok(view.find(node => node.props?.className === 'transcript-activity-content'), 'keep the payload for the exit animation');
  view.finishExit();
  assert.equal(view.find(node => node.props?.className === 'transcript-activity-content'), undefined);

  view.toggle();
  assert.ok(view.find(node => node.props?.className === 'transcript-activity-content'), 'the complete payload can be reopened');
});

test('image and file attachments share compact card sections and retain accessible open/download actions', () => {
  const attachments = [
    { id: 'image/id', name: 'figure.png', mimeType: 'image/png' },
    { id: 'document/id', name: 'report.pdf', mimeType: 'application/pdf' },
  ];
  for (const role of ['user', 'assistant']) {
    const message = {
      id: `message-${role}`, role, content: 'Files', attachments,
      artifact: role === 'assistant', time: '2026-10-02T12:00:00Z',
    };
    const messageView = disclosure('Message', { botId: 'bot/id', message });
    const attachedContent = messageView.find(node => typeof node.type === 'function' && node.type.name === 'Attachments');
    assert.ok(attachedContent, `${role} messages use the same attachment presentation`);
    assert.equal(attachedContent.props.attachments, attachments);
    const view = disclosure('Attachments', attachedContent.props);
    const cards = view.findAll(node => node.props?.className?.split(' ').includes('transcript-attachment'));
    assert.equal(cards.length, 2);

    for (let index = 0; index < attachments.length; index++) {
      const file = attachments[index];
      const classNames = cards[index].props.className.split(' ');
      assert.ok(classNames.includes(index === 0 ? 'is-image' : 'is-file'));
      const cardChildren = [cards[index].props.children].flat(Infinity).filter(Boolean);
      for (const className of ['transcript-attachment-preview', 'transcript-attachment-copy', 'transcript-attachment-download']) {
        assert.equal(cardChildren.filter(node => node.props?.className?.split(' ').includes(className)).length, 1,
          `${file.name} has one shared ${className} section`);
      }
      const download = cardChildren.find(node => node.props?.className?.split(' ').includes('transcript-attachment-download'));
      assert.equal(download.type, 'a');
      assert.equal(download.props.download, file.name);
      assert.equal(download.props['aria-label'], `Download ${file.name}`);
      assert.equal(download.props.href, `/api/studio/bots/bot%2Fid/files/${encodeURIComponent(file.id)}`);
    }
    const preview = view.find(node => node.type === 'a' && node.props['aria-label'] === 'Open figure.png');
    assert.ok(preview, 'the image preview keeps an accessible open action');
    assert.equal(preview.props.target, '_blank');
    assert.equal(preview.props.href, '/api/studio/bots/bot%2Fid/files/image%2Fid');
    assert.equal(view.find(node => node.type === 'img').props.alt, 'figure.png');
  }
});

test('a completed turn collapses its expanded history and can reopen the same actions', () => {
  const turn = {
    id: 'turn-1', status: 'running', users: [], responses: [], requests: [],
    activities: [activity], events: [{ seq: 1 }], notices: [], model: 'test-model',
  };
  const view = disclosure('TurnActivity', { turn, onPermission() {} });
  view.toggle();
  assert.equal(view.panel().props['aria-hidden'], false);
  assert.ok(view.find(node => node.props?.activity?.id === 'tool-1'));

  view.update({ turn: { ...turn, status: 'completed', activities: [{ ...activity, status: 'completed', output: '/workspace' }] } });
  assert.equal(view.panel().props['aria-hidden'], true);
  assert.equal(view.panel().props.inert, true);
  view.finishExit();
  assert.equal(view.find(node => node.props?.className === 'transcript-activity-list'), undefined);

  view.toggle();
  assert.equal(view.panel().props['aria-hidden'], false);
  assert.equal(view.find(node => node.props?.activity?.id === 'tool-1').props.activity.output, '/workspace');
  assert.ok(view.find(node => node.props?.events === turn.events), 'the raw journal remains available after completion');
});

test('completed turn details show the recorded service tier beside model and effort', () => {
  const turn = {
    id: 'turn-1', status: 'completed', backend: 'codex', model: 'gpt-test', effort: 'max', serviceTier: 'priority',
    users: [], responses: [], requests: [], activities: [], events: [], notices: [],
  };
  const view = disclosure('TurnActivity', { turn, onPermission() {} });
  view.toggle();
  assert.ok(view.find(node => node.type === 'span' && node.props.children === 'Codex · gpt-test · max · Fast'));

  view.update({ turn: { ...turn, serviceTier: 'custom-tier' } });
  assert.ok(view.find(node => node.type === 'span' && node.props.children === 'Codex · gpt-test · max · custom-tier'));
});

test('disclosure motion measures open content and collapses immediately for reduced motion', () => {
  const view = disclosure('ActivityItem', { activity, onPermission() {} }, { useReducedMotion: () => true });
  assert.equal(view.panel().props.initial, false);
  assert.equal(view.panel().props.animate.height, 0);
  assert.equal(view.panel().props.transition.duration, 0);
  view.toggle();
  assert.equal(view.panel().props.animate.height, 'auto');
  assert.equal(view.panel().props.animate.opacity, 1);
  view.toggle();
  assert.equal(view.panel().props.inert, true);
  assert.equal(view.panel().props.animate.height, 0);
  assert.deepEqual(view.timerDelays(), [0]);
  view.finishExit();
  assert.equal(view.find(node => node.props?.className === 'transcript-activity-content'), undefined);
});

test('raw data and journal panels keep lazy payloads while closing and expose accessible controls', () => {
  const event = { seq: 1, type: 'native', time: '2026-10-02T12:00:00Z', data: { method: 'item/completed' } };
  for (const [component, props, content] of [
    ['RawDetails', { value: { output: 'preserved' } }, node => node.props?.label === 'JSON'],
    ['JournalItem', { event }, node => node.props?.label === 'Event'],
    ['RawJournal', { events: [event] }, node => node.props?.className === 'transcript-journal-body'],
  ]) {
    const view = disclosure(component, props);
    assert.equal(view.panel().props.inert, true, `${component} starts closed`);
    assert.equal(view.find(content), undefined, `${component} does not mount hidden data`);
    view.toggle();
    assert.equal(view.panel().props['aria-hidden'], false);
    assert.ok(view.find(content));
    view.toggle();
    assert.equal(view.panel().props['aria-hidden'], true);
    assert.equal(view.panel().props.inert, true);
    assert.ok(view.find(content), `${component} retains data during its closing motion`);
    view.finishExit();
    assert.equal(view.find(content), undefined);
    view.toggle();
    assert.ok(view.find(content), `${component} can reopen the same data`);
  }
});

test('resolved and exiting request forms become inaccessible before their closing animation finishes', () => {
  const request = { id: 'request-1', title: 'Allow command', method: 'permission', questions: [], resolved: false };
  let present = true;
  const view = disclosure('RequestCard', { request, onPermission() {} }, { useIsPresent: () => present });
  const decision = () => view.find(node => node.props?.className === 'transcript-disclosure transcript-request-decision');
  const submit = () => view.find(node => node.type === 'button' && node.props.type === 'submit');
  assert.equal(decision().props.inert, false);
  assert.equal(submit().props.disabled, false);
  view.update({ request: { ...request, resolved: true } });
  assert.equal(decision().props.inert, true);
  assert.equal(decision().props['aria-hidden'], true);
  assert.equal(submit().props.disabled, true);
  assert.ok(view.find(node => node.type === 'form'), 'retain the form only for closing motion');
  view.finishExit();
  assert.equal(view.find(node => node.type === 'form'), undefined);

  view.update({ request });
  assert.equal(submit().props.disabled, false);
  present = false;
  view.update({});
  const card = view.find(node => node.props?.className === 'transcript-request');
  assert.equal(card.props.inert, true);
  assert.equal(card.props['aria-hidden'], true);
  assert.equal(decision().props.inert, true);
  assert.equal(submit().props.disabled, true);
});

test('animated goal budget retains accessible progress values and caps the fill at its budget', () => {
  const view = disclosure('Goal', { activity: { text: '', data: { tokensUsed: 250, tokenBudget: 200 } } }, { useReducedMotion: () => true });
  const progress = view.find(node => node.props?.role === 'progressbar');
  assert.equal(progress.props['aria-valuemin'], 0);
  assert.equal(progress.props['aria-valuemax'], 200);
  assert.equal(progress.props['aria-valuenow'], 200);
  assert.equal(progress.props['aria-label'], 'Tokens used from the goal budget');
  assert.equal(progress.props.children.props.initial, false);
  assert.equal(progress.props.children.props.animate.scaleX, 1);
  assert.equal(progress.props.children.props.transition.duration, 0);
});
