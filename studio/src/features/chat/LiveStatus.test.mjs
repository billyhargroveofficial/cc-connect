import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../../lib/motion-stub.mjs';

const source = ts.transpileModule(readFileSync(new URL('./LiveStatus.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
function queue(skills) {
  const element = (type, props) => ({ type, props });
  const modules = { react: { memo: component => component }, 'react/jsx-runtime': { jsx: element, jsxs: element },
    'lucide-react': new Proxy({}, { get: (_, name) => name }), '../../lib/chatStatus': {}, './ContextControl': { default: 'ContextControl' }, '../../lib/motion': motionTestModule() };
  const exports = {};
  runInNewContext(source, { exports, require: name => { assert.ok(name in modules); return modules[name]; } });
  const steered = [], removed = [];
  const tree = exports.QueuePanel({ snapshot: { paused: false, messages: [{ id: 'q', text: '', attachments: [], skills }] },
    pending: '', busy: true, suspended: false, error: '', onSteer: id => steered.push(id), onRemove: id => removed.push(id), onResume() {} });
  const nodes = [];
  const visit = node => { if (!node || typeof node !== 'object') return; nodes.push(node); const children = Array.isArray(node.props?.children) ? node.props.children : [node.props?.children]; children.flat(Infinity).forEach(visit); };
  visit(tree);
  return { tree, nodes, steered, removed };
}

test('queued skill-only messages retain compact names and accessible steer/remove actions', () => {
  const { tree, nodes, steered, removed } = queue([{ id: 'review', name: 'review', path: '/private/SKILL.md' }]);
  assert.deepEqual(Array.from(nodes.find(node => node.props?.className === 'queue-skill-chip').props.children), ['$', 'review']);
  assert.equal(JSON.stringify(tree).includes('/private/'), false, 'no filesystem paths enter visible queue DOM');
  nodes.find(node => node.props?.className === 'queue-steer').props.onClick();
  nodes.find(node => node.props?.className === 'queue-remove').props.onClick();
  assert.deepEqual(steered, ['q']); assert.deepEqual(removed, ['q']);
  assert.ok(nodes.find(node => node.props?.['aria-label'] === 'Steer queued message: review'));
});

test('session statusline mounts the native context control with progress and availability state', () => {
  const element = (type, props) => ({ type, props });
  const modules = { react: { memo: component => component }, 'react/jsx-runtime': { jsx: element, jsxs: element },
    'lucide-react': new Proxy({}, { get: (_, name) => name }), '../../lib/chatStatus': { effortLabel: effort => effort },
    './ContextControl': { default: 'ContextControl' }, '../../lib/motion': motionTestModule() };
  const exports = {};
  runInNewContext(source, { exports, require: name => { assert.ok(name in modules); return modules[name]; } });
  const context = { context: { percent: 25 }, compacting: true, requesting: false, supportsCompaction: true, compact() {} };
  const onError = () => {};
  const tree = exports.SessionStatus({ status: { step: 2, turn: 3, tokensPerSecond: 10 }, working: true, suspended: true, offline: true,
    context, model: 'Model', effort: 'max', tier: '', onError });
  const control = tree.props.children[3];
  assert.equal(control.type, 'ContextControl', 'manual compaction remains reachable in the statusline');
  assert.equal(control.props.state, context); assert.equal(control.props.busy, true);
  assert.equal(control.props.suspended, true); assert.equal(control.props.offline, true); assert.equal(control.props.onError, onError);
});

test('mobile queue rows bound many skills to one shrinking chip and an accessible remainder count', () => {
  const skills = Array.from({ length: 16 }, (_, index) => ({ id: `skill-${index}`, name: `long-skill-name-${index}`, path: `/private/${index}/SKILL.md` }));
  const { nodes } = queue(skills);
  assert.equal(nodes.filter(node => node.props?.className === 'queue-skill-chip').length, 1);
  const remainder = nodes.find(node => node.props?.className === 'queue-skill-more');
  assert.deepEqual(Array.from(remainder.props.children), ['+', 15]);
  assert.equal(remainder.props.title, skills.slice(1).map(skill => `$${skill.name}`).join(', '));
  assert.equal(remainder.props['aria-label'], `15 more attached skills: ${skills.slice(1).map(skill => skill.name).join(', ')}`);
  const row = nodes.find(node => node.props?.className === 'queued-message');
  assert.equal(row.props.children[2].props.className, 'queue-steer', 'Steer remains a separate row control outside clipped content');
  assert.equal(row.props.children[3].props.className, 'queue-remove');
  const css = readFileSync(new URL('./skill-picker.css', import.meta.url), 'utf8');
  assert.match(css, /\.queue-skill-chip\s*\{[^}]*flex-shrink:\s*1;[^}]*min-width:\s*0;[^}]*max-width:\s*105px;[^}]*overflow:\s*hidden;/);
});
