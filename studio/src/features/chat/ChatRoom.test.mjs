import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { createChatStatusProjector } from '../../lib/chatStatus.ts';
import { isWorking, statusLabel } from '../../lib/events.ts';
import { motionTestModule } from '../../lib/motion-stub.mjs';
import { skillMentionText } from './skillMentions.ts';

const source = ts.transpileModule(readFileSync(new URL('./ChatRoom.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
function chat({ receipt = { status: 'queued', turnId: 'active', queueId: 'q' } } = {}) {
  let cursor = 0, layoutCursor = 0, refreshed = 0;
  const values = [], layouts = [], pendingLayouts = [], calls = [];
  const state = initial => { const index = cursor++; if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial; return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }]; };
  const memo = (factory, dependencies) => { const index = cursor++; const old = values[index]; if (!old || dependencies.some((value, offset) => value !== old.dependencies[offset])) values[index] = { dependencies, value: factory() }; return values[index].value; };
  const element = (type, props) => ({ type, props });
  const queue = { snapshot: { messages: [], paused: false }, pending: '', error: '', binding: { accountId: 'account', nodeId: 'mac' },
    refresh: () => refreshed++, steer() {}, remove() {}, resume() {} };
  const modules = {
    react: { memo: component => component, lazy: () => 'Transcript', Suspense: 'fragment', useState: state, useRef: initial => state(() => ({ current: initial }))[0], useMemo: memo,
      useCallback: (callback, dependencies) => memo(() => callback, dependencies), useEffect() {},
      useLayoutEffect(callback, dependencies) { const index = layoutCursor++; const old = layouts[index]; if (!old || dependencies.some((value, offset) => value !== old.dependencies[offset])) pendingLayouts.push(() => { layouts[index] = { dependencies }; callback(); }); },
    },
    'react/jsx-runtime': { jsx: element, jsxs: element, Fragment: 'fragment' },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../../lib/api': { api: { send: async (...args) => { calls.push(args); return receipt; }, stop: async () => {} }, errorMessage: error => error.message },
    '../../lib/events': { isWorking, statusLabel }, '../../lib/chatStatus': { createChatStatusProjector },
    '../../lib/motion': motionTestModule(), '../../components/Avatar': { default: 'Avatar' },
    './Composer': { default: 'Composer' }, './GoalPanel': { GoalDialog: 'GoalDialog', useGoal: () => ({ goal: null, setGoal() {} }) },
    '../../hooks/useBotContext': { useBotContext: () => ({ context: { percent: 25 }, compacting: false, requesting: false }) },
    './BotIsland': { default: 'BotIsland' }, './LiveStatus': { WorkingStrip: 'WorkingStrip', SessionStatus: 'SessionStatus', QueuePanel: 'QueuePanel' },
    './useMessageQueue': { useMessageQueue: () => queue },
    './skillMentions': { skillMentionText },
  };
  const exports = {};
  runInNewContext(source, { exports, require: name => { assert.ok(name in modules, `Unexpected import ${name}`); return modules[name]; } });
  const props = { bot: { id: 'bot', status: 'running', backend: 'codex', model: 'model', effort: 'max', name: 'Bot' }, draftScope: 'account:mac',
    events: [{ seq: 1, botId: 'bot', turnId: 'active', type: 'turn', time: '2026-10-02T10:00:00Z', data: { status: 'running' } }], messages: [],
    capabilities: { backends: {}, models: [] }, loading: false, suspended: false, onBack() {}, onBotChange() {}, onArchive() {}, onError() {} };
  const render = () => { cursor = 0; layoutCursor = 0; const tree = exports.default(props); for (const callback of pendingLayouts.splice(0)) callback(); return tree; };
  function find(match) {
    function visit(node) { if (!node || typeof node !== 'object') return; if (match(node)) return node; const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children]; for (const child of children.flat(Infinity)) { const found = visit(child); if (found) return found; } }
    return visit(render());
  }
  return { calls, find, refreshed: () => refreshed, props,
    send: (text = 'Next task', skills = []) => find(node => node.type === 'Composer').props.onSend(text, [], skills),
  };
}

test('busy message submission explicitly queues for the bound account and node without scrolling the reader', async () => {
  const view = chat();
  const scroll = view.find(node => node.props?.className === 'chat-scroll');
  const container = { scrollHeight: 1000, scrollTop: 100, clientHeight: 400 };
  scroll.props.ref.current = container;
  scroll.props.onScroll();
  await view.send();
  assert.equal(view.calls[0][3], 'queue');
  assert.deepEqual(view.calls[0][4], { accountId: 'account', nodeId: 'mac' });
  assert.equal(view.refreshed(), 1);
  view.props.events = [...view.props.events, { seq: 2, botId: 'bot', turnId: 'active', type: 'native', time: '2026-10-02T10:00:01Z', data: { method: 'item/agentMessage/delta' } }];
  view.find(() => false);
  assert.equal(container.scrollTop, 100, 'sending into queue preserves an earlier reading position');
});

test('Working is outside history and directly adjacent above composer with statusline below', () => {
  const view = chat();
  const stack = view.find(node => node.props?.className === 'chat-input-stack');
  assert.deepEqual(Array.from(stack.props.children, child => child.type), ['QueuePanel', 'WorkingStrip', 'Composer', 'SessionStatus']);
  const composer = view.find(node => node.type === 'Composer');
  const statusline = stack.props.children[3];
  assert.equal(statusline.props.context, stack.props.children[2].props.context, 'statusline receives the action and native progress, not just the percentage');
  assert.equal(statusline.props.onError, view.props.onError);
  assert.equal(composer.props.busy, true);
  assert.equal(composer.props.onStop, undefined, 'the working strip owns stop while send queues');
  const css = readFileSync(new URL('./minimal-composer.css', import.meta.url), 'utf8');
  assert.match(css, /\.working-strip\s*\{[^}]*margin:\s*0;/);
  assert.match(css, /\.chat-input-stack \.composer-wrap\.composer-minimal\s*\{[^}]*padding-bottom:\s*0;/);
  assert.match(css, /\.session-statusline\s*\{[^}]*flex-wrap:\s*nowrap;[^}]*overflow-x:\s*auto;/);
});

test('native skill attachments retain identity through the queue while older hosts get text mentions', async () => {
  const skills = [{ id: 'skill-id', name: 'review', path: '/private/project/review/SKILL.md' }];
  const native = chat();
  native.props.capabilities.skillAttachments = true;
  await native.send('Review this.', skills);
  assert.equal(native.calls[0][1], 'Review this.');
  assert.equal(native.calls[0][3], 'queue');
  assert.equal(native.calls[0][5], skills);
  const legacy = chat();
  await legacy.send('Review this.', skills);
  assert.equal(legacy.calls[0][1], '$review\nReview this.');
  assert.equal(legacy.calls[0][5].length, 0, 'older hosts never receive an unsupported skills field');
});
