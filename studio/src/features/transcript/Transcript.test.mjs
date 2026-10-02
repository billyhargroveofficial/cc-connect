import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import * as reducer from './reducer.ts';
import { motionTestModule } from '../../lib/motion-stub.mjs';
import { accountURL, setApiAccount, setApiNode } from '../../lib/api.ts';

const source = ts.transpileModule(
  `${readFileSync(new URL('./Transcript.tsx', import.meta.url), 'utf8')}\nexport { ActivityItem, TurnActivity, TurnHistory, TurnStats, ResponseDetails, Turn, Attachments, Message, ProgressMessage, RawDetails, JournalItem, RawJournal, RequestCard, Goal };`,
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
      memo: component => component,
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
    './reducer': reducer, './transcript.css': {}, 'katex/dist/katex.min.css': {},
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

test('sent user skill references remain compact named chips beside text and files', () => {
  const message = { id: 'user', role: 'user', content: 'Review this.', attachments: [], time: '', skills: [{ id: 'shared-review', name: 'review' }, { id: 'project-writer', name: 'writer' }] };
  const view = disclosure('Message', { botId: 'bot', message });
  assert.equal(view.find(node => node.props?.className === 'transcript-skill-chips').props['aria-label'], 'Attached skills');
  assert.deepEqual(view.findAll(node => node.props?.className === 'transcript-skill-chip').map(node => node.props.children[1]), ['review', 'writer']);
  const onlySkills = disclosure('Message', { botId: 'bot', message: { ...message, content: '' } });
  assert.equal(onlySkills.findAll(node => node.props?.className === 'transcript-skill-chip').length, 2, 'skill-only messages are visible');
});

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

test('native attachment media, downloads, and markdown artifact links retain their account and node binding', () => {
  setApiAccount('account-a');
  try {
    for (const nodeId of ['local', 'mac-a']) {
      setApiNode(nodeId);
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
        if (url) {
          const parsed = new URL(url, 'http://connect-bots.local');
          assert.equal(parsed.searchParams.get('expectedAccount'), 'account-a');
          assert.equal(parsed.searchParams.get('node'), nodeId);
        }
      }
      const markdown = disclosure('Markdown', { content: '' }).find(node => node.props?.components)?.props.components;
      const local = '/api/studio/bots/bot-a/files/image';
      const bound = `${local}?expectedAccount=account-a&node=${nodeId}`;
      assert.equal(markdown.a({ href: local, children: 'Open' }).props.href, bound);
      const image = markdown.img({ src: local, alt: 'Figure' });
      assert.equal(image.props.href, bound);
      assert.equal(image.props.children.props.src, bound);
      assert.equal(markdown.a({ href: 'https://example.com', children: 'Reference' }).props.href, 'https://example.com');
    }
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

test('resolved approvals remain between the tools they originally separated in an expanded batch', () => {
  const turn = {
    id: 'turn-1', status: 'completed', users: [], responses: [], events: [], notices: [],
    activities: [{ ...activity, id: 'first', seq: 1 }, { ...activity, id: 'last', seq: 3 }],
    requests: [{ id: 'approval', seq: 2, title: 'Allow command', method: 'permission', questions: [], resolved: true }],
  };
  const view = disclosure('TurnActivity', { turn, batch: true, onPermission() {} });
  view.toggle();
  assert.deepEqual(view.findAll(node => ['ActivityItem', 'RequestCard'].includes(node.type?.name))
    .map(node => node.props.activity?.id || node.props.request?.id), ['first', 'approval', 'last']);
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

test('only the last response receives compact details even when its turn contains tool history', () => {
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
  assert.equal(view.find(node => node.type?.name === 'TurnActivity'), undefined,
    'a completed response without actions does not retain a standalone details row');

  for (const changes of [
    { activities: [{ ...activity, status: 'completed' }] },
    { requests: [{ id: 'request-1', title: 'Allow command', method: 'permission', questions: [], resolved: true }] },
    { status: 'failed' },
    { status: 'stopped' },
  ]) {
    const nextTurn = { ...turn, ...changes };
    view.update({ turn: nextTurn });
    assert.equal(responses()[0].props.details, undefined);
    assert.equal(responses()[1].props.details.props.turn, nextTurn, 'the final response owns the full journal and stats');
    if (changes.activities || changes.requests) {
      const history = view.find(node => node.type?.name === 'TurnHistory');
      assert.ok(history, 'real actions fold into one compact history before the final answer');
      const archived = disclosure('TurnHistory', history.props);
      archived.toggle();
      assert.equal(archived.find(node => node.type?.name === 'TurnActivity').props.batch, true);
    }
  }
  view.update({ turn: { ...turn, status: 'running' } });
  assert.ok(responses().every(message => !message.props.details), 'live responses do not repeat completion metadata');
});

test('completed progress and tool batches fold into one lazy disclosure before the final answer and reopen in start order', () => {
  const turn = {
    id: 'turn-1', status: 'running', backend: 'codex', model: 'gpt-test', effort: 'max', serviceTier: '',
    time: '2026-10-02T12:00:00Z', users: [], requests: [], notices: [], events: [{ seq: 1, time: '2026-10-02T12:01:05Z' }],
    activities: [
      { ...activity, id: 'a', seq: 1, status: 'completed', output: 'first payload' },
      { ...activity, id: 'p', seq: 2, kind: 'commentary', title: 'Progress message', text: 'I found the cause.', status: 'completed' },
      { ...activity, id: 'b', seq: 3, status: 'completed', output: 'second payload' },
    ],
    responses: [],
  };
  const view = disclosure('Turn', { bot: { id: 'bot-1' }, turn, onPermission() {} });
  const content = () => view.findAll(node => ['TurnHistory', 'TurnActivity', 'ProgressMessage', 'Message'].includes(node.type?.name));
  assert.deepEqual(content().map(node => node.type.name), ['TurnActivity', 'ProgressMessage', 'TurnActivity']);
  assert.equal(content()[1].props.activity.text, 'I found the cause.');
  assert.equal(content()[1].props.active, true);
  const withAnswer = { ...turn, responses: [{ id: 'answer', role: 'assistant', seq: 4, content: 'Fixed.', attachments: [], time: '' }] };
  view.update({ turn: withAnswer });
  assert.deepEqual(content().map(node => node.type.name), ['TurnActivity', 'ProgressMessage', 'TurnActivity', 'Message'],
    'a live Pi text segment may precede more tools, so it cannot prematurely hide progress');
  const finished = { ...withAnswer, status: 'completed' };
  view.update({ turn: finished });
  assert.deepEqual(content().map(node => node.type.name), ['TurnHistory', 'Message']);
  assert.equal(content()[1].props.details.props.turn, finished);
  const history = disclosure('TurnHistory', content()[0].props);
  const toggle = () => history.find(node => node.type === 'button' && node.props['aria-controls']);
  assert.equal(toggle().props['aria-expanded'], false);
  assert.equal(toggle().props['aria-label'], 'Worked for 1m 5s: 2 actions · 1 update');
  assert.equal(toggle().props['aria-controls'], history.panel().props.id);
  assert.equal(history.panel().props.inert, true);
  assert.equal(history.find(node => node.props?.className === 'transcript-history-content'), undefined, 'archived Markdown and tool payloads stay unmounted');
  history.toggle();
  assert.equal(history.panel().props['aria-hidden'], false);
  assert.equal(history.panel().props.inert, false);
  assert.equal(history.find(node => node.props?.className === 'transcript-history-content').props['aria-label'], 'Previous activity');
  const archived = history.findAll(node => ['TurnActivity', 'ProgressMessage'].includes(node.type?.name));
  assert.deepEqual(archived.map(node => node.type.name), ['TurnActivity', 'ProgressMessage', 'TurnActivity']);
  assert.equal(archived[1].props.activity, turn.activities[1]);
  assert.equal(archived[1].props.active, false);
  for (const [index, id] of [[0, 'a'], [2, 'b']]) {
    const group = disclosure('TurnActivity', archived[index].props);
    assert.equal(group.find(node => node.type === 'button' && node.props['aria-controls']).props['aria-expanded'], false);
    assert.equal(group.panel().props.inert, true);
    assert.equal(group.find(node => node.type?.name === 'ActivityItem'), undefined, 'collapsed tool data stays lazy');
    group.toggle();
    assert.equal(group.find(node => node.type?.name === 'ActivityItem').props.activity.id, id);
    assert.equal(group.find(node => node.type?.name === 'TurnStats'), undefined, 'stats are kept beside the final response');
    assert.equal(group.find(node => node.type?.name === 'RawJournal'), undefined, 'the full journal is kept beside the final response');
  }
  history.toggle();
  assert.equal(history.panel().props['aria-hidden'], true);
  assert.equal(history.panel().props.inert, true, 'closing history immediately leaves the keyboard order');
  assert.ok(history.find(node => node.type?.name === 'ProgressMessage'), 'retain the content only for the exit animation');
  history.finishExit();
  assert.equal(history.find(node => node.type?.name === 'ProgressMessage'), undefined);
  history.toggle();
  assert.equal(history.find(node => node.type?.name === 'ProgressMessage').props.activity.text, 'I found the cause.');
});

test('service narration and action batches leave the keyboard order as their automatic folding exit starts', () => {
  const turn = { id: 'turn-1', status: 'running', users: [], responses: [], requests: [], activities: [activity], events: [], notices: [] };
  const motion = { useIsPresent: () => false };
  for (const [component, props, className] of [
    ['ProgressMessage', { activity: { ...activity, kind: 'commentary', text: '[Source](https://example.com)' }, active: false }, 'transcript-progress-message'],
    ['TurnActivity', { turn, onPermission() {} }, 'transcript-turn-activity is-running'],
  ]) {
    const view = disclosure(component, props, motion);
    const root = view.find(node => node.props?.className === className);
    assert.equal(root.props.inert, true, `${component} cannot retain keyboard focus in its exiting subtree`);
    assert.equal(root.props['aria-hidden'], true);
  }
});

test('empty live turns leave the sole Working strip to the composer while terminal status stays inspectable', () => {
  const turn = { id: 'turn-1', status: 'running', users: [], responses: [], requests: [], activities: [], events: [], notices: [] };
  const view = disclosure('Turn', { bot: { id: 'bot-1' }, turn, onPermission() {} });
  assert.equal(view.find(node => node.type?.name === 'TurnActivity'), undefined, 'live turns do not repeat Working above the composer');
  for (const status of ['starting', 'queued', 'waiting_permission']) {
    view.update({ turn: { ...turn, status } });
    assert.equal(view.find(node => node.type?.name === 'TurnActivity'), undefined);
  }
  for (const status of ['failed', 'stopped', 'interrupted']) {
    view.update({ turn: { ...turn, status } });
    assert.equal(view.find(node => node.type?.name === 'TurnActivity').props.turn.status, status);
  }
});

test('progress-only completed turns retain local details without adding standalone ellipsis to notice-only turns', () => {
  const turn = {
    id: 'turn-1', status: 'completed', backend: 'codex', model: 'gpt-test', effort: 'max', serviceTier: '',
    users: [], requests: [], responses: [], notices: [], events: [{ seq: 1 }],
    activities: [{ ...activity, id: 'progress', seq: 1, kind: 'commentary', text: 'Work is done.', status: 'completed' }],
  };
  const view = disclosure('Turn', { bot: { id: 'bot-1' }, turn, onPermission() {} });
  const progress = view.find(node => node.type?.name === 'ProgressMessage');
  assert.equal(progress.props.details?.type.name, 'ResponseDetails');
  assert.equal(progress.props.details.props.turn, turn);
  assert.equal(view.find(node => node.type?.name === 'TurnActivity'), undefined, 'narration does not create an extra service row');
  const line = disclosure('ProgressMessage', progress.props);
  assert.ok(line.find(node => node.props?.className === 'transcript-message-actions'));
  const details = disclosure('ResponseDetails', progress.props.details.props);
  details.toggle();
  assert.equal(details.find(node => node.type?.name === 'TurnStats').props.turn, turn);
  assert.equal(details.find(node => node.type?.name === 'RawJournal').props.events, turn.events);

  const noticeTurn = { ...turn, activities: [], notices: ['Session resumed.'] };
  view.update({ turn: noticeTurn });
  assert.equal(view.find(node => node.type?.name === 'ResponseDetails'), undefined,
    'service-only notices do not add an orphan details button to the dialogue');
  assert.equal(view.find(node => node.props?.className === 'transcript-message-actions'), undefined);
  assert.ok(view.find(node => node.type?.name === 'Markdown' && node.props.content === 'Session resumed.'),
    'meaningful notices remain visible');
  assert.equal(view.find(node => node.props?.className === 'transcript-bot-response'), undefined);
  assert.equal(view.find(node => node.type?.name === 'TurnActivity'), undefined);
});

test('session configuration history does not mount a notice or standalone ellipsis in the conversation', () => {
  const events = [
    { seq: 1, botId: 'bot-1', turnId: '', type: 'system', time: '', data: {
      content: 'Instructions or skills were updated. A new Codex session was created with the conversation history carried over.',
      backend: 'codex', threadId: 'new', previousThreadId: 'old',
    } },
    { seq: 2, botId: 'bot-1', turnId: '', type: 'system', time: '', data: { content: 'Connection lost; retry required.' } },
  ];
  const view = disclosure('Transcript', { bot: { id: 'bot-1', name: 'Assistant' }, events, onPermission() {} });
  const turns = view.findAll(node => node.type?.name === 'Turn');
  assert.equal(turns.length, 1, 'history-only events do not produce a visible chat turn');
  assert.deepEqual(turns[0].props.turn.notices, ['Connection lost; retry required.']);
  const notice = disclosure('Turn', turns[0].props);
  assert.equal(notice.find(node => node.type?.name === 'ResponseDetails'), undefined);
  assert.equal(notice.find(node => node.props?.className === 'transcript-message-actions'), undefined);
  assert.ok(notice.find(node => node.type?.name === 'Markdown' && node.props.content === 'Connection lost; retry required.'));
  const raw = reducer.buildTranscript(events, 'bot-1');
  assert.deepEqual(raw.flatMap(turn => turn.events), events, 'hidden history remains available to the bot island');
});

test('response details prefer the last ordinary answer while artifact-only turns remain inspectable', () => {
  const answer = { id: 'answer', role: 'assistant', seq: 2, content: 'Created.', attachments: [], time: '' };
  const artifact = { id: 'files', role: 'assistant', seq: 3, content: 'Files', artifact: true, time: '',
    attachments: [{ id: 'file', name: 'result.txt', mimeType: 'text/plain' }] };
  const turn = {
    id: 'turn-1', status: 'completed', backend: 'codex', model: 'gpt-test', effort: 'max', serviceTier: '',
    users: [], requests: [], activities: [], responses: [answer, artifact], notices: [], events: [{ seq: 1 }],
  };
  const view = disclosure('Turn', { bot: { id: 'bot-1' }, turn, onPermission() {} });
  const messages = () => view.findAll(node => node.type?.name === 'Message');
  assert.equal(messages().find(node => node.props.message.id === 'answer').props.details?.type.name, 'ResponseDetails');
  assert.equal(messages().find(node => node.props.message.id === 'files').props.details, undefined);
  view.update({ turn: { ...turn, responses: [artifact] } });
  assert.equal(messages()[0].props.details?.type.name, 'ResponseDetails');
});

test('conversation omits the global All bot events row while local turn details stay available', () => {
  const events = [{ seq: 1, botId: 'bot-1', turnId: 'turn-1', type: 'message', time: '',
    data: { role: 'assistant', content: 'Done.' } },
  { seq: 2, botId: 'bot-1', turnId: 'turn-1', type: 'turn', time: '', data: { status: 'completed' } }];
  const view = disclosure('Transcript', { bot: { id: 'bot-1', name: 'Assistant' }, events, onPermission() {} });
  assert.equal(view.find(node => node.props?.title === 'All bot events'), undefined);
  const turn = view.find(node => node.type?.name === 'Turn');
  const local = disclosure('Turn', turn.props);
  assert.equal(local.find(node => node.type?.name === 'Message').props.details?.type.name, 'ResponseDetails');
});

test('long histories mount the latest turns first and hydrate older turns in bounded batches', () => {
  const events = [];
  for (let index = 0; index < 50; index++) {
    const turnId = `turn-${index}`;
    events.push({ seq: index * 2 + 1, botId: 'bot-1', turnId, type: 'message', time: '',
      data: { role: 'assistant', content: `Answer ${index}` } });
    events.push({ seq: index * 2 + 2, botId: 'bot-1', turnId, type: 'turn', time: '', data: { status: 'completed' } });
  }
  const view = disclosure('Transcript', { bot: { id: 'bot-1', name: 'Assistant' }, events, onPermission() {} });
  const mounted = () => view.findAll(node => node.type?.name === 'Turn').length;
  assert.equal(mounted(), 12);
  view.finishExit();
  assert.equal(mounted(), 24);
  view.finishExit(); assert.equal(mounted(), 36);
  view.finishExit(); assert.equal(mounted(), 48);
  view.finishExit();
  assert.equal(mounted(), 50);
});

test('expanded action batches use an unruled list without a separator gutter', () => {
  const css = readFileSync(new URL('./transcript.css', import.meta.url), 'utf8');
  const rules = [...css.matchAll(/\.transcript-activity-list\{([^}]*)\}/g)].map(match => match[1]);
  assert.ok(rules.length);
  assert.ok(rules.every(rule => !/border(?:-left)?:/.test(rule)));
  assert.match(rules[0], /margin:2px 0 2px 0/);
  assert.match(rules[0], /padding:0 0 2px 0/);
});

test('only the latest inline progress uses lightweight streaming text while waiting requests remain actionable', () => {
  const progress = seq => ({ ...activity, id: `progress-${seq}`, seq, kind: 'commentary', text: `Progress ${seq}`, status: 'completed' });
  const request = { id: 'request', seq: 4, title: 'Approval required', method: 'permission', questions: [], resolved: false };
  const childRequest = { ...request, id: 'child-request' };
  const turn = {
    id: 'turn-1', status: 'running', backend: 'codex', model: '', effort: '', serviceTier: '',
    users: [], responses: [], requests: [], notices: [], events: [],
    activities: [progress(1), { ...activity, id: 'tool', seq: 2 }, progress(3)],
  };
  const view = disclosure('Turn', { bot: { id: 'bot-1' }, turn, onPermission() {} });
  const progressLines = () => view.findAll(node => node.type?.name === 'ProgressMessage');
  assert.deepEqual(progressLines().map(node => node.props.active), [false, true]);
  const line = disclosure('ProgressMessage', progressLines()[1].props);
  assert.equal(line.find(node => node.props?.['data-progress-id'] === 'progress-3').props.className, 'transcript-progress-message is-active');
  assert.equal(line.find(node => node.type?.name === 'StreamingText').props.content, 'Progress 3');

  view.update({ turn: { ...turn, requests: [request], activities: [...turn.activities,
    { ...activity, id: 'child', kind: 'subagent', seq: 5, thread: { ...turn, id: 'child', activities: [], requests: [childRequest] } }],
  } });
  assert.deepEqual(progressLines().map(node => node.props.active), [false, false], 'waiting on the owner settles streaming narration');
  assert.deepEqual(view.findAll(node => node.type?.name === 'RequestCard').map(node => node.props.request.id), ['request', 'child-request'],
    'both parent and nested pending requests stay outside closed batches');

  view.update({ turn: { ...turn, status: 'completed' } });
  assert.ok(progressLines().every(node => !node.props.active));
  line.update({ active: false });
  assert.equal(line.find(node => node.props?.['data-progress-id'] === 'progress-3').props.className, 'transcript-progress-message');
  assert.equal(line.find(node => node.type?.name === 'Markdown').props.content, 'Progress 3');
});

test('inline progress stays plain while service controls retain reduced motion and mobile touch targets', () => {
  const css = readFileSync(new URL('./transcript.css', import.meta.url), 'utf8');
  assert.doesNotMatch(css, /progress-shimmer|background-clip:text|color:transparent/,
    'the composer owns the sole Working shimmer; progress text does not animate');
  assert.match(css, /\.transcript-progress-message\{[^{}]*color:var\(--text/);
  const reduced = css.slice(css.indexOf('@media(prefers-reduced-motion:reduce)'));
  assert.match(reduced, /\.transcript-spin\{animation:none\}/);
  assert.match(reduced, /\.transcript-history-toggle[^{}]*\{transition:none\}/);
  const mobile = css.slice(css.indexOf('@media(max-width:700px)'));
  assert.match(mobile, /\.transcript-turn-activity\.is-batch \.transcript-activity-toggle\{min-height:44px\}/);
});

test('plain progress text stays readable in both themes', () => {
  const themes = readFileSync(new URL('../../styles.css', import.meta.url), 'utf8');
  const [darkTheme, lightTheme] = themes.split(':root[data-theme="light"]');
  const variable = (theme, name) => theme.match(new RegExp(`${name}:\\s*(#[\\da-f]{3,6})\\b`, 'i'))[1];
  const luminance = color => {
    const hex = color.slice(1);
    const channels = (hex.length === 3 ? [...hex].map(value => value.repeat(2)).join('') : hex).match(/../g)
      .map(value => parseInt(value, 16) / 255)
      .map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4);
    return channels[0] * .2126 + channels[1] * .7152 + channels[2] * .0722;
  };
  const contrast = (foreground, background) => {
    const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
    return (values[0] + .05) / (values[1] + .05);
  };
  assert.ok(contrast(variable(darkTheme, '--text'), variable(darkTheme, '--bg')) >= 4.5, 'dark progress retains text contrast');
  assert.ok(contrast(variable(lightTheme, '--text'), variable(lightTheme, '--bg')) >= 4.5, 'light progress retains text contrast');
});

test('transcript surfaces omit visible border rules while preserving focus outlines and semantic separators', () => {
  const css = readFileSync(new URL('./transcript.css', import.meta.url), 'utf8');
  assert.doesNotMatch(css, /(?:^|[;{])border(?:-(?:top|right|bottom|left))?:\s*(?!0(?:px)?(?:\b|;))[^;}]+/,
    'cards, payloads, requests, tables and journals use surfaces instead of visible borders');
  for (const selector of ['transcript-history-toggle', 'transcript-question-option', 'transcript-free-answer input', 'transcript-button', 'transcript-journal-toolbar input']) {
    assert.match(css, new RegExp(`\\.${selector.replaceAll('.', '\\.')}:focus-(?:visible|within)[^{}]*\\{outline:2px`),
      `${selector} keeps a visible keyboard focus indicator`);
  }
  assert.match(css, /\.transcript-markdown hr\{border:0;height:0;margin:/, 'Markdown separators retain their semantic DOM and spacing');
  assert.match(css, /\.transcript-markdown tbody tr:nth-child\(2n\)\{background:/, 'borderless tables retain alternating row surfaces');
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
