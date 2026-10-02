import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import * as reducer from './reducer.ts';

const source = ts.transpileModule(
  `${readFileSync(new URL('./Transcript.tsx', import.meta.url), 'utf8')}\nexport { ActivityItem, TurnActivity, Attachments, Message };`,
  { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX } },
).outputText;

// Exercise the disclosure's actual handlers and hooks, including the exit
// timer. Rendering and markdown internals are outside this state test.
function disclosure(component, initialProps) {
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
      setTimeout: callback => { timers.set(++nextTimer, callback); return nextTimer; },
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
      callbacks.forEach(callback => callback());
    },
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
