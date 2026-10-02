import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { mergeEvents } from './events.ts';
import { botHistoryEntry } from './botHistory.ts';

const source = ts.transpileModule(readFileSync(new URL('./eventJournal.ts', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText;
const journalModule = {};
runInNewContext(source, { exports: journalModule, require: name => ({ './events': { mergeEvents }, './botHistory': { botHistoryEntry } })[name] });
const { EventJournal, emptyJournal } = journalModule;

const event = (seq, botId = 'a', type = 'native') => ({ seq, botId, type, time: '', data: {} });
const lifecycle = (seq, botId = 'a') => ({ ...event(seq, botId, 'system'), data: {
  content: 'Instructions or skills were updated. A new Codex session was created with the conversation history carried over.',
} });

test('journals notify only the changed bot and keep roster tokens silent', () => {
  const journal = new EventJournal();
  let a = 0, b = 0, roster = 0;
  journal.subscribe('a', () => a++);
  journal.subscribe('b', () => b++);
  journal.subscribeRoster(() => roster++);
  journal.merge([event(1), event(2)]);
  assert.equal(a, 1);
  assert.equal(b, 0);
  assert.equal(roster, 0);
  const snapshot = journal.forBot('a');
  journal.merge([event(2)]);
  assert.equal(journal.forBot('a'), snapshot);
  assert.equal(a, 1);
  journal.merge([event(3, 'b', 'message')]);
  assert.equal(a, 1);
  assert.equal(b, 1);
  assert.equal(roster, 1);
});

test('history can be assigned to a requested bot and clear invalidates live projections', () => {
  const journal = new EventJournal();
  let bot = 0, roster = 0;
  journal.subscribe('requested', () => bot++);
  journal.subscribeRoster(() => roster++);
  journal.merge([event(1, 'stale', 'message')], 'requested');
  assert.equal(journal.forBot('stale'), emptyJournal);
  assert.equal(journal.forBot('requested').length, 1);
  journal.clear();
  assert.equal(journal.forBot('requested'), emptyJournal);
  assert.equal(bot, 2);
  assert.equal(roster, 2);
});

test('bot change history retains its snapshot during token bursts, tools, user text and replay', () => {
  const journal = new EventJournal();
  const change = lifecycle(2);
  journal.merge([event(1), change, lifecycle(3, 'b')]);
  const history = journal.historyFor('a');
  assert.equal(history.length, 1);
  assert.equal(history[0], change);
  assert.equal(journal.historyFor('b').length, 1);
  assert.equal(journal.historyFor('missing'), emptyJournal);
  journal.merge([event(4), event(5, 'a', 'agent'), { ...lifecycle(6), type: 'message', data: { ...change.data, role: 'user' } }]);
  assert.equal(journal.historyFor('a'), history, 'inference and quoted user text cannot wake the island');
  journal.merge([{ ...change, data: { ...change.data } }]);
  assert.equal(journal.historyFor('a'), history, 'equivalent history replay preserves the slice identity');
});

test('lifecycle corrections update or remove history without deleting raw journal records', () => {
  const journal = new EventJournal();
  const original = lifecycle(2);
  journal.merge([event(1), original]);
  const first = journal.historyFor('a');
  const updated = { ...original, time: '2026-10-02T17:00:00Z' };
  journal.merge([updated]);
  assert.notEqual(journal.historyFor('a'), first);
  assert.equal(journal.historyFor('a')[0], updated);
  const corrected = { ...updated, data: { content: 'Provider connection failed.' } };
  journal.merge([corrected]);
  assert.equal(journal.historyFor('a').length, 0);
  assert.equal(journal.forBot('a')[1], corrected, 'the provider warning remains in the canonical journal');
  const empty = journal.historyFor('a');
  journal.merge([event(3)]);
  assert.equal(journal.historyFor('a'), empty);
  journal.merge([original]);
  assert.equal(journal.historyFor('a')[0], original, 'a later corrected lifecycle receipt is restored');
});

test('overlapping lifecycle corrections use the last event for each sequence', () => {
  const journal = new EventJournal();
  const receipt = lifecycle(1);
  const corrected = { ...receipt, data: { content: 'Provider connection failed.' } };
  journal.merge([receipt, corrected]);
  assert.equal(journal.historyFor('a').length, 0);
  journal.merge([corrected, receipt]);
  assert.equal(journal.historyFor('a')[0], receipt);
  journal.merge([corrected, receipt]);
  assert.equal(journal.historyFor('a')[0], receipt);
  journal.merge([receipt, corrected]);
  assert.equal(journal.historyFor('a').length, 0);
});

test('fixed bot assignment and account clearing retain lifecycle history isolation', () => {
  const journal = new EventJournal();
  const receipt = lifecycle(1, 'legacy-id');
  journal.merge([receipt], 'requested');
  assert.equal(journal.historyFor('legacy-id'), emptyJournal);
  assert.equal(journal.historyFor('requested')[0], receipt);
  journal.clear();
  assert.equal(journal.historyFor('requested'), emptyJournal);
  journal.merge([event(2, 'requested')]);
  assert.equal(journal.historyFor('requested'), emptyJournal, 'clearing also discards old lifecycle sequence membership');
});
