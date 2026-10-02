import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { botHistoryEntry } from '../../lib/botHistory.ts';
import { motionTestModule } from '../../lib/motion-stub.mjs';

const source = ts.transpileModule(readFileSync(new URL('./BotChangeHistory.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const content = 'Instructions or skills were updated. A new Codex session was created with the conversation history carried over.';
const change = (seq, text = content) => ({ seq, botId: 'bot', type: 'system', time: `2026-10-02T17:${String(seq).padStart(2, '0')}:00Z`, data: { content: text } });

function fixture(events = []) {
  let cursor = 0, lookups = 0;
  const values = [];
  const state = initial => {
    const index = cursor++;
    if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
    return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
  };
  const memo = (factory, dependencies) => {
    const index = cursor++;
    const old = values[index];
    if (!old || dependencies.some((value, offset) => value !== old.dependencies[offset]))
      values[index] = { dependencies, value: factory() };
    return values[index].value;
  };
  const element = (type, props, key) => ({ type, props, key });
  const modules = {
    react: { memo: component => component, useState: state, useMemo: memo },
    'react/jsx-runtime': { jsx: element, jsxs: element },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    '../../lib/botHistory': { botHistoryEntry(event) { lookups++; return botHistoryEntry(event); } },
    '../../lib/motion': motionTestModule(),
  };
  const exports = {};
  runInNewContext(source, { exports, require: name => { assert.ok(name in modules, `Unexpected import ${name}`); return modules[name]; } });
  const props = { botId: 'bot', events };
  const render = () => { cursor = 0; return exports.default(props); };
  function visit(root, match) {
    if (!root || typeof root !== 'object') return;
    if (match(root)) return root;
    const children = Array.isArray(root.props?.children) ? root.props.children : [root.props?.children];
    for (const child of children.flat(Infinity)) { const found = visit(child, match); if (found) return found; }
  }
  const find = match => visit(render(), match);
  const list = () => find(node => node.props?.className === 'bot-history-list');
  return { props, render, find, lookups: () => lookups,
    toggle: () => find(node => node.props?.className === 'bot-history-toggle').props.onClick(),
    rows: () => list()?.props.children || [],
  };
}

test('bot changes are hidden behind a compact accessible island disclosure by default', () => {
  const view = fixture([change(1)]);
  const button = view.find(node => node.props?.className === 'bot-history-toggle');
  assert.equal(button.props['aria-expanded'], false);
  assert.equal(button.props['aria-controls'], 'bot-change-history-bot');
  const panel = view.find(node => node.props?.id === button.props['aria-controls']);
  assert.equal(panel.props['aria-hidden'], true);
  assert.equal(panel.props.inert, true);
  assert.equal(panel.props.layout, undefined, 'disclosure must not add layout projection to inference updates');
  view.toggle();
  const expanded = view.find(node => node.props?.id === button.props['aria-controls']);
  assert.equal(expanded.props['aria-hidden'], false);
  assert.equal(expanded.props.inert, false);
  assert.equal(expanded.props.animate.gridTemplateRows, '1fr');
  view.toggle();
  assert.equal(view.find(node => node.props?.id === button.props['aria-controls']).props.inert, true);
});

test('history preserves full English lifecycle details and timestamps in newest-first order', () => {
  const legacy = change(1, 'Настройки инструкций или навыков обновлены. Создана новая сессия Codex с переносом истории разговора.');
  const recovered = change(2, "The Pi session has not been saved yet, or its file is missing. A new session was created while preserving the bot's visible history.");
  const view = fixture([legacy, recovered]);
  const rows = view.rows();
  assert.deepEqual(Array.from(rows, row => row.key), [2, 1]);
  assert.equal(rows[0].props.children[0].props.children, 'Session recovered');
  assert.equal(rows[1].props.children[0].props.children, 'Instructions or skills updated');
  assert.equal(rows[1].props.children[2].props.children, content);
  assert.equal(rows[1].props.children[1].props.dateTime, legacy.time);
  assert.match(rows[1].props.children[1].props.children, /Oct\s+2/);
});

test('island mounts only the latest page and can reveal every older lifecycle entry', () => {
  const view = fixture(Array.from({ length: 25 }, (_, index) => change(index + 1)));
  view.toggle();
  assert.equal(view.rows().length, 10);
  const classified = view.lookups();
  view.find(node => node.props?.className === 'bot-history-more').props.onClick();
  assert.equal(view.rows().length, 20);
  assert.equal(view.lookups(), classified, 'paging never reclassifies the journal');
  view.find(node => node.props?.className === 'bot-history-more').props.onClick();
  assert.equal(view.rows().length, 25);
  assert.equal(view.find(node => node.props?.className === 'bot-history-more'), undefined);
  const ids = Array.from(view.rows(), row => row.key);
  assert.equal(ids.at(0), 25);
  assert.equal(ids.at(-1), 1);
});

test('history projection trusts journal scope and remains idle while its event slice is stable', () => {
  const legacy = { ...change(1), botId: 'legacy-id' };
  const view = fixture([legacy]);
  view.props.botId = 'requested';
  assert.equal(view.rows().length, 1, 'fixedBotId ingestion owns scope even when the raw event keeps its old identity');
  const classified = view.lookups();
  view.toggle();
  view.render();
  assert.equal(view.lookups(), classified, 'local disclosure changes use the stable projection');
  view.props.events = [...view.props.events, change(2)];
  assert.equal(view.rows().length, 2);
});

test('empty history, quoted notices and provider warnings do not create a lifecycle section', () => {
  assert.equal(fixture().render(), null);
  const ownerText = { ...change(1), type: 'message', data: { role: 'user', content } };
  assert.equal(fixture([ownerText, change(2, 'Provider connection failed.')]).render(), null);
  const invalidTime = fixture([{ ...change(1), time: 'missing' }]);
  assert.ok(!invalidTime.rows()[0].props.children[1]);
});

test('history controls retain a touch target on mobile without adding separators', () => {
  const css = readFileSync(new URL('./bot-island.css', import.meta.url), 'utf8');
  assert.match(css, /@media\s*\(max-width:\s*1099px\)[\s\S]*\.bot-history-toggle,\s*\.bot-history-more\s*\{[^}]*min-height:\s*44px;/);
  const blocks = [...css.matchAll(/\.bot-history[^}]+\{[^}]+\}/g)].map(match => match[0]).join('\n');
  assert.doesNotMatch(blocks, /border(?:-\w+)?:/);
});
