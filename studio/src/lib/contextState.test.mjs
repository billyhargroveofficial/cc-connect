import assert from 'node:assert/strict';
import test from 'node:test';
import { contextNumbers, contextEventRevision, compactionState, contextCompacting } from './contextState.ts';

const native = (seq, method, params = {}, rootThreadId = 'root') => ({
  seq, type: 'native', botId: 'bot', data: { backend: 'codex', method, params, rootThreadId },
});
const action = (seq, status, backend = 'codex') => ({
  seq, type: 'compact_action', botId: 'bot', data: { backend, status, requestId: 'op' },
});

test('context usage distinguishes an actual zero from unknown provider values', () => {
  assert.equal(contextNumbers(null).percent, undefined);
  assert.equal(contextNumbers({compacting:false, contextWindow:200000}).percent, undefined);
  assert.equal(contextNumbers({compacting:false, usedTokens:0, contextWindow:200000}).percent, 0);
  assert.equal(contextNumbers({compacting:false, usedTokens:40000, contextWindow:200000}).percent, 20);
  assert.equal(contextNumbers({compacting:false, usedTokens:200, percent:12.5}).percent, 12.5);
  assert.equal(contextNumbers({compacting:false, usedTokens:-1, contextWindow:Infinity}).percent, undefined);
});

test('root compaction reflects actual native start and completion without child contamination', () => {
  const start = native(2, 'item/started', {threadId:'root', item:{id:'compact',type:'contextCompaction'}});
  const child = native(3, 'item/completed', {threadId:'child', item:{id:'childCompact',type:'contextCompaction'}});
  assert.equal(compactionState([start,child], 'codex', 'root', 1), true);
  const done = native(4, 'item/completed', {threadId:'root', item:{id:'compact',type:'contextCompaction'}});
  assert.equal(compactionState([start,child,done], 'codex', 'root', 1), false);
  assert.equal(compactionState([start,child,done], 'codex', 'root', 4), undefined);
  assert.equal(compactionState([start], 'codex', 'fresh', 1), undefined);
});

test('manual compaction uses tracked backend lifecycle and Pi uses real auto compaction events', () => {
  assert.equal(compactionState([action(2,'starting')], 'codex', 'root', 0), true);
  assert.equal(compactionState([action(2,'starting'),action(3,'completed')], 'codex', 'root', 0), false);
  assert.equal(compactionState([action(2,'starting'),action(3,'error')], 'codex', 'root', 0), false);
  assert.equal(compactionState([action(2,'started')], 'codex', 'root', 0), true);
  assert.equal(compactionState([action(2,'started'),action(3,'failed')], 'codex', 'root', 0), false);
  assert.equal(compactionState([action(2,'starting','pi')], 'codex', 'root', 0), undefined);
  const pi = (seq,method) => ({seq,type:'native',data:{backend:'pi',method,params:{}}});
  assert.equal(compactionState([pi(1,'auto_compaction_start')], 'pi', 'pi-thread', 0), true);
  assert.equal(compactionState([pi(1,'auto_compaction_start'),pi(2,'auto_compaction_end')], 'pi', 'pi-thread', 0), false);
  assert.equal(compactionState([pi(3,'compaction_start')], 'pi', 'pi-thread', 0), true);
  assert.equal(compactionState([pi(3,'compaction_start'),pi(4,'compaction_end')], 'pi', 'pi-thread', 0), false);
});

test('context refresh triggers on real usage/completion and ignores child and token deltas', () => {
  const usage = native(5,'thread/tokenUsage/updated',{threadId:'root'});
  const child = native(6,'turn/completed',{threadId:'child'});
  const delta = native(7,'item/agentMessage/delta',{threadId:'root',delta:'token'});
  assert.equal(contextEventRevision([usage,child,delta], 'codex', 'root'), 5);
  assert.equal(contextEventRevision([action(9,'completed')], 'codex', 'root'), 9);
});
test('native item completion keeps the manual-operation spinner until the tracked receipt', () => {
  const start = action(2,'started');
  const itemDone = native(3,'item/completed',{threadId:'root',item:{type:'contextCompaction'}});
  assert.equal(contextCompacting(false,[start,itemDone],'codex','root',1),true);
  assert.equal(contextCompacting(true,[start,itemDone],'codex','root',2),true);
  assert.equal(contextCompacting(false,[start,itemDone],'codex','root',2),false);
  assert.equal(contextCompacting(true,[start,itemDone,action(4,'completed')],'codex','root',2),false);
  assert.equal(contextCompacting(true,[start,itemDone,action(4,'failed')],'codex','root',2),false);
  const auto = native(5,'item/started',{threadId:'root',item:{type:'contextCompaction'}});
  assert.equal(contextCompacting(false,[start,action(4,'completed'),auto],'codex','root',4),true);
});
