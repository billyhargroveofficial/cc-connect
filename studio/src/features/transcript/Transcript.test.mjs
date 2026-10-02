import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import * as reducer from './reducer.ts';
import { motionTestModule } from '../../lib/motion-stub.mjs';
import { accountURL, setApiAccount } from '../../lib/api.ts';

const source = ts.transpileModule(
  `${readFileSync(new URL('./Transcript.tsx', import.meta.url), 'utf8')}\nexport { ActivityItem, TurnActivity, TurnStats, ResponseDetails, Turn, Attachments, Message, RawDetails, JournalItem, RawJournal, RequestCard, Goal };`,
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
    '../../lib/api': { accountURL },
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

test('native attachment media, downloads, and markdown artifact links retain their account binding', () => {
  setApiAccount('account-a');
  try {
    const view = disclosure('Attachments', {
      botId: 'bot-a',
      attachments: [
        { id: 'image', name: 'image.png', mimeType: 'image/png' },
        { id: 'audio', name: 'audio.wav', mimeType: 'audio/wav' },
        { id: 'video', name: 'video.mp4', mimeType: 'video/mp4' },
      ],
    });
    for (const node of view.findAll(node => node.type === 'a' || ['img', 'audio', 'video'].includes(node.type))) {
      const url = node.props.href || node.props.src;
      if (url) assert.match(url, /\?expectedAccount=account-a$/);
    }
    const markdown = disclosure('Markdown', { content: '' }).find(node => node.props?.components)?.props.components;
    const local = '/api/studio/bots/bot-a/files/image';
    assert.equal(markdown.a({ href: local, children: 'Open' }).props.href, `${local}?expectedAccount=account-a`);
    const image = markdown.img({ src: local, alt: 'Figure' });
    assert.equal(image.props.href, `${local}?expectedAccount=account-a`);
    assert.equal(image.props.children.props.src, `${local}?expectedAccount=account-a`);
    assert.equal(markdown.a({ href: 'https://example.com', children: 'Reference' }).props.href, 'https://example.com');
  } finally { setApiAccount(null); }
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

test('completed tool history retains the Activity disclosure, resolved requests, and recorded metadata', () => {
  const events = [{ seq: 1, type: 'native', time: '2026-10-02T12:00:00Z', data: { method: 'item/completed' } }];
  const request = { id: 'request-1', title: 'Allow command', method: 'permission', questions: [], resolved: true };
  const turn = {
    id: 'turn-1', status: 'completed', backend: 'codex', model: 'gpt-test', effort: 'max', serviceTier: 'priority',
    outputTokens: 1234, tokensPerSecond: 45.67, generationMs: 2500,
    users: [{ id: 'user-1' }], responses: [{ id: 'response-1' }], requests: [request],
    activities: [{ ...activity, status: 'completed', output: '/workspace' }], events, notices: [],
  };
  const onPermission = () => {};
  const onQuestion = () => {};
  const view = disclosure('TurnActivity', { turn, onPermission, onQuestion });
  const toggle = view.find(node => node.type === 'button' && node.props['aria-controls']);
  assert.equal(toggle.props['aria-expanded'], false);
  assert.equal(toggle.props['aria-controls'], view.panel().props.id);
  assert.ok(view.find(node => node.type === 'span' && node.props.children === 'Activity'));
  assert.ok(view.find(node => node.props?.['aria-label'] === 'Action count: 1'));
  assert.equal(view.find(node => node.type?.name === 'TurnStats'), undefined, 'recorded data stays lazy');

  view.toggle();
  const tool = view.find(node => node.props?.activity?.id === 'tool-1');
  assert.equal(tool.props.activity.output, '/workspace');
  assert.equal(tool.props.onPermission, onPermission);
  assert.equal(tool.props.onQuestion, onQuestion);
  const resolved = view.find(node => node.props?.request?.id === request.id);
  assert.equal(resolved.props.request, request);
  assert.equal(resolved.props.onPermission, onPermission);
  assert.equal(resolved.props.onQuestion, onQuestion);
  const metadata = view.find(node => node.type?.name === 'TurnStats');
  assert.equal(metadata.props.turn, turn);
  const stats = disclosure('TurnStats', metadata.props);
  assert.ok(stats.find(node => node.type === 'span' && node.props.children === 'Codex · gpt-test · max · Fast'));
  assert.ok(stats.find(node => node.type === 'span' && node.props.children?.[0] === '≈ 45.7'));
  assert.ok(stats.find(node => node.type === 'span' && node.props.children?.[0] === '1,234'));
  assert.ok(stats.find(node => node.type === 'span' && node.props.children?.[0] === '2.5 s'));
  assert.equal(view.find(node => node.type?.name === 'RawJournal').props.events, events);
});

test('running activity retains its live status while response metadata stays deferred', () => {
  const turn = {
    id: 'turn-1', status: 'running', backend: 'codex', model: 'gpt-test', effort: 'max', serviceTier: 'priority',
    outputTokens: 100, tokensPerSecond: 25, generationMs: 4000,
    users: [], responses: [], requests: [], activities: [activity], events: [], notices: [],
  };
  const view = disclosure('TurnActivity', { turn, onPermission() {} });
  const status = view.find(node => node.props?.role === 'status');
  assert.equal(status.props['aria-live'], 'polite');
  assert.equal(status.props.title, 'Running command');
  view.toggle();
  assert.ok(view.find(node => node.props?.activity?.id === activity.id));
  assert.equal(view.find(node => node.type?.name === 'TurnStats'), undefined);
});

test('the compact response details action exposes the recorded stats and journal without losing disclosure accessibility', () => {
  const events = [{ seq: 1, type: 'native', time: '2026-10-02T12:00:00Z', data: { method: 'turn/completed' } }];
  const turn = {
    id: 'turn-1', status: 'completed', backend: 'codex', model: 'gpt-test', effort: 'max', serviceTier: 'priority',
    outputTokens: 1234, tokensPerSecond: 45.67, generationMs: 2500,
    users: [], responses: [], requests: [], activities: [], events, notices: [],
  };
  const view = disclosure('ResponseDetails', { turn });
  const action = () => view.find(node => node.type === 'button' && node.props['aria-label'] === 'Response details');
  const contentId = action().props['aria-controls'];
  const panel = () => view.find(node => node.props?.id === contentId);
  const stats = () => view.find(node => node.type?.name === 'TurnStats');
  const journal = () => view.find(node => node.type?.name === 'RawJournal');
  assert.equal(action().props.type, 'button');
  assert.equal(action().props.title, 'Response details');
  assert.ok(contentId, 'the icon action names the panel that it controls');
  assert.equal(action().props['aria-expanded'], false);
  assert.equal(panel().props['aria-hidden'], true);
  assert.equal(panel().props.inert, true);
  assert.equal(stats(), undefined);
  assert.equal(journal(), undefined);

  view.toggle();
  assert.equal(action().props['aria-expanded'], true);
  assert.equal(action().props['aria-controls'], contentId);
  assert.equal(panel().props['aria-hidden'], false);
  assert.equal(panel().props.inert, false);
  assert.equal(stats().props.turn, turn);
  const metadata = disclosure('TurnStats', stats().props);
  assert.ok(metadata.find(node => node.type === 'span' && node.props.children === 'Codex · gpt-test · max · Fast'));
  assert.ok(metadata.find(node => node.type === 'span' && node.props.children?.[0] === '≈ 45.7'));
  assert.ok(metadata.find(node => node.type === 'span' && node.props.children?.[0] === '1,234'));
  assert.ok(metadata.find(node => node.type === 'span' && node.props.children?.[0] === '2.5 s'));
  assert.equal(journal().props.events, events);

  view.toggle();
  assert.equal(action().props['aria-expanded'], false);
  assert.equal(panel().props['aria-hidden'], true);
  assert.equal(panel().props.inert, true, 'closing details immediately leave the keyboard tab order');
  assert.ok(stats(), 'metadata remains mounted for the closing animation');
  assert.equal(journal().props.events, events);
  view.finishExit();
  assert.equal(stats(), undefined);
  assert.equal(journal(), undefined);
  view.toggle();
  assert.ok(stats());
  assert.equal(journal().props.events, events, 'reopening preserves the complete turn journal');

  view.update({ turn: { ...turn, serviceTier: 'custom-tier' } });
  const updatedMetadata = disclosure('TurnStats', stats().props);
  assert.ok(updatedMetadata.find(node => node.type === 'span' && node.props.children === 'Codex · gpt-test · max · custom-tier'));
});

test('response details collapse immediately for reduced motion', () => {
  const turn = {
    id: 'turn-1', status: 'completed', backend: 'codex', model: 'gpt-test', effort: 'max', serviceTier: '',
    users: [], responses: [], requests: [], activities: [], events: [], notices: [],
  };
  const view = disclosure('ResponseDetails', { turn }, { useReducedMotion: () => true });
  const contentId = view.find(node => node.type === 'button' && node.props['aria-label'] === 'Response details').props['aria-controls'];
  const panel = () => view.find(node => node.props?.id === contentId);
  view.toggle();
  assert.equal(panel().props.animate.height, 'auto');
  assert.equal(panel().props.transition.duration, 0);
  view.toggle();
  assert.equal(panel().props.animate.height, 0);
  assert.equal(panel().props.inert, true);
  assert.deepEqual(view.timerDelays(), [0]);
  view.finishExit();
  assert.equal(view.find(node => node.type?.name === 'TurnStats'), undefined);
});

test('assistant response actions retain Copy and place optional details beside it', () => {
  const details = { type: 'details-test', props: { children: 'Recorded turn' } };
  const message = { id: 'response-1', role: 'assistant', content: 'The response', attachments: [], time: '2026-10-02T12:00:00Z' };
  const view = disclosure('Message', { botId: 'bot-1', message, details });
  const actions = view.find(node => node.props?.className === 'transcript-message-actions');
  assert.ok(actions);
  assert.ok([actions.props.children].flat(Infinity).includes(details), 'details share the existing response action row');
  const copy = view.find(node => node.type?.name === 'CopyButton');
  assert.equal(copy.props.content, message.content);
  assert.equal(copy.props.label, 'Copy response');
  assert.ok(view.find(node => node.type?.name === 'Markdown' && node.props.content === message.content));

  const attachmentOnly = disclosure('Message', {
    botId: 'bot-1', details,
    message: { ...message, content: '', attachments: [{ id: 'file-1', name: 'result.txt', mimeType: 'text/plain' }] },
  });
  assert.ok(attachmentOnly.find(node => node.props?.className === 'transcript-message-actions'),
    'attachment-only answers still expose their turn details');
  assert.equal(attachmentOnly.find(node => node.type?.name === 'CopyButton'), undefined);
});

test('only the last response of a completed metadata-only turn receives compact details', () => {
  const messages = [
    { id: 'response-1', role: 'assistant', content: 'First', attachments: [], time: '2026-10-02T12:00:00Z' },
    { id: 'response-2', role: 'assistant', content: 'Final', attachments: [], time: '2026-10-02T12:00:01Z' },
  ];
  const turn = {
    id: 'turn-1', status: 'completed', backend: 'codex', model: 'gpt-test', effort: 'max', serviceTier: 'priority',
    users: [], responses: messages, requests: [], activities: [], events: [], notices: [],
  };
  const view = disclosure('Turn', { bot: { id: 'bot-1' }, turn, onPermission() {} });
  const responses = () => view.findAll(node => node.type?.name === 'Message' && node.props.message.role === 'assistant');
  assert.equal(responses().length, 2);
  assert.equal(responses()[0].props.details, undefined);
  assert.equal(responses()[1].props.details?.type.name, 'ResponseDetails');
  assert.equal(responses()[1].props.details.props.turn, turn);
  const activityView = disclosure('TurnActivity', view.find(node => node.type?.name === 'TurnActivity').props);
  assert.equal(activityView.find(node => node.props?.className?.startsWith('transcript-turn-activity')), undefined,
    'a completed response without actions does not retain a standalone details row');

  for (const changes of [
    { activities: [{ ...activity, status: 'completed' }] },
    { requests: [{ id: 'request-1', title: 'Allow command', method: 'permission', questions: [], resolved: true }] },
    { status: 'running' },
    { status: 'failed' },
    { status: 'stopped' },
  ]) {
    const nextTurn = { ...turn, ...changes };
    view.update({ turn: nextTurn });
    assert.ok(responses().every(message => !message.props.details), 'running work and real history keep their Activity disclosure');
    assert.equal(view.find(node => node.type?.name === 'TurnActivity').props.turn, nextTurn);
  }
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
