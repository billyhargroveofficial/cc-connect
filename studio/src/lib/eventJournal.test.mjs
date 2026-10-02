import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { mergeEvents } from './events.ts';

const source = ts.transpileModule(readFileSync(new URL('./eventJournal.ts', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText;
const journalModule = {};
runInNewContext(source, { exports: journalModule, require: name => name === './events' ? { mergeEvents } : undefined });
const { EventJournal, emptyJournal } = journalModule;

const event = (seq, botId = 'a', type = 'native') => ({ seq, botId, type, time: '', data: {} });

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
