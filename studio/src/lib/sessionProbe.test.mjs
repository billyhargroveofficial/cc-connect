import assert from 'node:assert/strict';
import test from 'node:test';
import {createSessionProbe} from './sessionProbe.ts';
function fixture(check) {const calls=[];let expired=0;const cancelled=[];const probe=createSessionProbe({check,expired:()=>expired++,schedule:(fn,delay)=>{calls.push({fn,delay});return calls.length;},cancel:id=>cancelled.push(id)});return {probe,calls,cancelled,get expired(){return expired}};}
const flush=()=>new Promise(resolve=>setImmediate(resolve));
test('repeated SSE failures schedule one session check and require login after expiry',async()=>{const f=fixture(async()=>({authenticated:false}));f.probe.reconnect();f.probe.reconnect();assert.equal(f.calls.length,1);assert.equal(f.calls[0].delay,4000);f.calls[0].fn();await flush();assert.equal(f.expired,1);});
test('server outages retain login and back off bounded probes',async()=>{const f=fixture(async()=>{throw new Error('offline')});for(let i=0;i<7;i++){f.probe.reconnect();f.calls.at(-1).fn();await flush();}assert.equal(f.expired,0);assert.deepEqual(f.calls.map(c=>c.delay),[4000,8000,16000,30000,30000,30000,30000]);});
test('successful reconnect cancels delayed session check',async()=>{let checked=0;const f=fixture(async()=>{checked++;return {authenticated:true}});f.probe.reconnect();f.probe.connected();assert.deepEqual(f.cancelled,[1]);assert.equal(checked,0);f.probe.reconnect();assert.equal(f.calls.at(-1).delay,4000);f.probe.dispose();assert.deepEqual(f.cancelled,[1,2]);});
test('unmounted stream ignores a late authentication result',async()=>{let resolve;const f=fixture(()=>new Promise(r=>{resolve=r}));f.probe.reconnect();f.calls[0].fn();f.probe.dispose();resolve({authenticated:false});await flush();assert.equal(f.expired,0);});
