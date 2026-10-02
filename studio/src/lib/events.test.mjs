import assert from 'node:assert/strict';
import test from 'node:test';
import {mergeBots,mergeEvents,telegramLabel,botMessagePresentation,messagePreview} from './events.ts';
const bot=(status,updatedAt)=>({id:'one',name:'One',status,updatedAt});
test('stale SSE replay cannot replace newer bot snapshot or revive archived bot',()=>{const current=bot('archived','2026-10-01T12:00:05Z');const previous=bot('running','2026-10-01T12:00:01Z');assert.deepEqual(mergeBots([current],[previous]),[current]);assert.equal(mergeBots([previous],[current])[0].status,'archived');});
test('newer SSE bot survives a concurrent earlier GET response',()=>{const current=bot('idle','2026-10-01T12:00:05Z');const stale=bot('running','2026-10-01T12:00:04Z');assert.equal(mergeBots([current],[stale])[0].status,'idle');});
test('journal replay merges duplicate seq and out of order snapshot',()=>{const one={seq:1},two={seq:2},three={seq:3};assert.deepEqual(mergeEvents([one,three],[two,three]),[one,two,three]);assert.deepEqual(mergeEvents([one],[two]),[one,two]);});
test('enabling Telegram never claims a live connection before manager confirms it',()=>{assert.equal(telegramLabel({enabled:true,status:'connecting'}),'Connecting to Telegram');assert.equal(telegramLabel({enabled:true,status:'error'}),'Telegram connection error');assert.equal(telegramLabel({enabled:true,status:'connected'}),'Connected to Telegram');});

test('Go nanosecond timestamps preserve order across variable fractional precision',()=>{const earlier=bot('running','2026-10-01T12:00:00.00012Z');const later=bot('idle','2026-10-01T12:00:00.000123Z');assert.equal(mergeBots([later],[earlier])[0].status,'idle');assert.equal(mergeBots([earlier],[later])[0].status,'idle');});
test('trusted bot handoff renders a sender label while preserving user text and mismatched metadata',()=>{const content='Message from bot Руководитель (chief-id):\nВычисли 7×8\nБез инструментов.';assert.deepEqual(botMessagePresentation(content,'bot:chief-id'),{sender:'Руководитель',content:'Вычисли 7×8\nБез инструментов.'});assert.equal(botMessagePresentation(content,'web'),null);assert.equal(botMessagePresentation(content,'bot:other'),null);assert.equal(botMessagePresentation('Unchanged text','bot:chief-id'),null);});
test('service context and persistence metadata never replace a real roster message preview',()=>{const answer={seq:1,type:'message',data:{role:'assistant',content:'Готово',source:'codex'}};const internal={seq:2,type:'message',data:{role:'user',content:'Internal bounded context acknowledgement',source:'goal_context'}};const saved={seq:3,type:'system',data:{content:'Session history saved.'}};assert.equal(messagePreview([answer,internal,saved]),'Готово');assert.equal(messagePreview([internal,saved]),'');assert.equal(messagePreview([{...internal,data:{...internal.data,source:'web'}}]),internal.data.content);});

test('identical replay and snapshots preserve journal and bot references', () => {
  const events = [{ seq: 1, data: { content: 'one' } }, { seq: 2, data: { content: 'two' } }];
  assert.equal(mergeEvents(events, structuredClone(events)), events);
  const changed = mergeEvents(events, [{ seq: 2, data: { content: 'corrected' } }]);
  assert.equal(changed[0], events[0]);
  assert.equal(changed[1].data.content, 'corrected');
  const bots = [bot('idle', '2026-10-02T10:00:00Z')];
  assert.equal(mergeBots(bots, structuredClone(bots)), bots);
  assert.equal(mergeBots(bots, [bot('idle', '2026-10-01T10:00:00Z')]), bots);
  assert.equal(mergeBots(bots, [{ ...bots[0], name: 'Renamed' }])[0].name, 'Renamed');
});

test('stream batches append once and still sort overlapping or unordered delivery', () => {
  const events = [{ seq: 1 }, { seq: 2 }];
  assert.deepEqual(mergeEvents(events, [{ seq: 3 }, { seq: 4 }]).map(e => e.seq), [1, 2, 3, 4]);
  assert.deepEqual(mergeEvents(events, [{ seq: 4 }, { seq: 2 }, { seq: 3 }]).map(e => e.seq), [1, 2, 3, 4]);
});
