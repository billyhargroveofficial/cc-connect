import assert from 'node:assert/strict';
import test from 'node:test';
import { api } from './api.ts';

test('native clear response preserves its baseline for newer goal replay', async () => {
  const original = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (path, options) => {
    calls.push({ path, method: options.method });
    return new Response(JSON.stringify({ cleared: true, cursor: 19 }), { status: 200 });
  };
  try {
    assert.deepEqual(await api.clearGoal('my/bot'), { goal: null, cursor: 19 });
    assert.deepEqual(calls, [{ path: '/api/studio/bots/my%2Fbot/goal', method: 'DELETE' }]);
  } finally {
    globalThis.fetch = original;
  }
});

test('an invalid clear response cannot silently erase a pinned goal', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({ cursor: 19 }), { status: 200 });
  try {
    await assert.rejects(api.clearGoal('bot'), /неожиданный ответ/);
  } finally {
    globalThis.fetch = original;
  }
});
