import assert from 'node:assert/strict';
import test from 'node:test';
import { api, accountURL, fileURL, nodeBinaryURL, onApiAccountChanged, setApiAccount, setApiNode } from './api.ts';

test('queue and steer use the exact native contract and captured workspace identity', async () => {
  const original = globalThis.fetch, calls = [];
  setApiAccount('active-account'); setApiNode('active-node');
  globalThis.fetch = async (path, options) => { calls.push({ path, options }); return new Response(JSON.stringify({ turnId: 'turn', status: 'queued', queueId: 'message', messages: [], paused: false }), { status: 200 }); };
  const binding = { accountId: 'captured-account', nodeId: 'captured-mac' };
  try {
    await api.send('bot/a', 'Queued instruction', [], 'queue', binding);
    await api.queue('bot/a', undefined, binding);
    await api.steerQueued('bot/a', 'message/b', binding);
    await api.removeQueued('bot/a', 'message/b', binding);
    await api.resumeQueue('bot/a', binding);
    assert.deepEqual(calls.map(call => [call.path, call.options.method || 'GET']), [
      ['/api/studio/bots/bot%2Fa/messages', 'POST'], ['/api/studio/bots/bot%2Fa/queue', 'GET'],
      ['/api/studio/bots/bot%2Fa/queue/message%2Fb/steer', 'POST'], ['/api/studio/bots/bot%2Fa/queue/message%2Fb', 'DELETE'],
      ['/api/studio/bots/bot%2Fa/queue/resume', 'POST'],
    ]);
    assert.deepEqual(JSON.parse(calls[0].options.body), { text: 'Queued instruction', attachments: [], mode: 'queue' });
    assert.ok(calls.every(call => call.options.headers.get('X-Connect-Bots-Account') === 'captured-account'));
    assert.ok(calls.every(call => call.options.headers.get('X-Connect-Bots-Node') === 'captured-mac'));
  } finally { setApiAccount(null); globalThis.fetch = original; }
});

test('account APIs use same-origin cookies and explicit username/password payloads', async () => {
  const original = globalThis.fetch;
  const calls = [];
  const session = { authenticated: true, user: { id: 'user-1', username: 'billy' }, registrationAllowed: true };
  globalThis.fetch = async (path, options) => {
    calls.push({ path, method: options.method || 'GET', body: options.body, credentials: options.credentials, headers: options.headers });
    return new Response(JSON.stringify(session), { status: 200 });
  };
  try {
    assert.deepEqual(await api.session(), session);
    assert.deepEqual(await api.login({ username: 'billy', password: ' pass word ' }), session);
    assert.deepEqual(await api.register({ username: 'new.user', password: 'password' }), session);
    await api.logout('user-1');
    assert.deepEqual(calls.map(({ path, method, credentials }) => ({ path, method, credentials })), [
      { path: '/api/studio/session', method: 'GET', credentials: 'same-origin' },
      { path: '/api/studio/login', method: 'POST', credentials: 'same-origin' },
      { path: '/api/studio/register', method: 'POST', credentials: 'same-origin' },
      { path: '/api/studio/logout', method: 'POST', credentials: 'same-origin' },
    ]);
    assert.deepEqual(JSON.parse(calls[1].body), { username: 'billy', password: ' pass word ' });
    assert.deepEqual(JSON.parse(calls[2].body), { username: 'new.user', password: 'password' });
    assert.equal(calls[1].headers.get('Content-Type'), 'application/json');
    assert.equal(calls[1].headers.has('Authorization'), false);
    assert.equal(calls[3].headers.get('X-Connect-Bots-Account'), 'user-1');
    assert.ok(calls.every(call => !call.headers.has('X-Connect-Bots-Node')), 'account endpoints never route to a workspace node');
  } finally {
    globalThis.fetch = original;
  }
});

test('every tenant request binds to its account and a changed-cookie mutation is never replayed', async () => {
  const original = globalThis.fetch;
  const calls = [];
  const changed = [];
  const unsubscribe = onApiAccountChanged(account => { changed.push(account); setApiAccount(null); });
  setApiAccount('account-a');
  globalThis.fetch = async (path, options) => {
    calls.push({ path, options });
    return new Response(JSON.stringify({ code: 'account_changed', error: 'Account session changed.' }), { status: 409 });
  };
  try {
    await assert.rejects(api.saveInstructions(undefined, 'A private instructions'), error => error.code === 'account_changed');
    assert.equal(calls.length, 1, 'the mutation is issued once and is never automatically replayed under B');
    assert.equal(calls[0].options.headers.get('X-Connect-Bots-Account'), 'account-a');
    assert.equal(calls[0].options.method, 'PUT');
    assert.deepEqual(changed, ['account-a']);
    await assert.rejects(api.saveMaintenance({ enabled: false }), /Sign in again/);
    assert.equal(calls.length, 1, 'protected requests stop until another account is verified');
    setApiAccount('account-b');
    globalThis.fetch = async (path, options) => {
      calls.push({ path, options });
      return new Response(JSON.stringify({ id: 'uploaded' }), { status: 200 });
    };
    await api.upload('bot-b', new File(['hello'], 'hello.txt', { type: 'text/plain' }));
    assert.equal(calls[1].options.headers.get('X-Connect-Bots-Account'), 'account-b');
    assert.ok(calls[1].options.body instanceof FormData, 'multipart uploads use the same account binding');
    assert.equal(calls[1].options.headers.has('Content-Type'), false, 'the browser provides the multipart boundary');
  } finally {
    unsubscribe();
    setApiAccount(null);
    globalThis.fetch = original;
  }
});

test('native file URLs carry an account precondition while external URLs stay unchanged', () => {
  setApiAccount('account/a');
  setApiNode('mac/book');
  try {
    assert.equal(fileURL('bot', { id: 'file', name: 'test.png', mimeType: 'image/png' }), '/api/studio/bots/bot/files/file?expectedAccount=account%2Fa&node=mac%2Fbook');
    assert.equal(fileURL('bot', { id: 'file', url: '/api/studio/bots/bot/files/file?download=1#preview' }), '/api/studio/bots/bot/files/file?download=1&expectedAccount=account%2Fa&node=mac%2Fbook#preview');
    assert.equal(accountURL('https://example.com/api/studio/files/photo.png'), 'https://example.com/api/studio/files/photo.png');
    assert.equal(accountURL('/assets/logo.svg'), '/assets/logo.svg');
  } finally { setApiAccount(null); }
});

test('workspace API headers bind mutations, files and catalogs to a node while node management stays account scoped', async () => {
  const original = globalThis.fetch;
  const calls = [];
  setApiAccount('account-a');
  setApiNode('mac-a');
  globalThis.fetch = async (path, options) => {
    calls.push({ path, options });
    return new Response(JSON.stringify({ nodes: [], code: 'one-time-code' }), { status: 200 });
  };
  try {
    await api.bots();
    await api.send('shared-bot', 'Hello');
    await api.upload('shared-bot', new File(['private'], 'note.txt'));
    await api.capabilities(undefined, undefined, 'mac-b');
    await api.createBot({ name: 'On another host' }, 'mac-b');
    await api.bots();
    await api.nodes();
    await api.createNodeEnrollment('MacBook');
    await api.removeNode('mac/b');
    await api.logout('account-a');
    assert.deepEqual(calls.slice(0, 6).map(call => call.options.headers.get('X-Connect-Bots-Node')), [
      'mac-a', 'mac-a', 'mac-a', 'mac-b', 'mac-b', 'mac-a',
    ], 'explicit create/catalog targets do not change the active workspace');
    assert.ok(calls.every(call => call.options.headers.get('X-Connect-Bots-Account') === 'account-a'));
    assert.ok(calls.slice(6).every(call => !call.options.headers.has('X-Connect-Bots-Node')));
    assert.equal(calls[7].path, '/api/studio/nodes/enrollments');
    assert.deepEqual(JSON.parse(calls[7].options.body), { name: 'MacBook' });
    assert.equal(calls[8].path, '/api/studio/nodes/mac%2Fb');
    assert.equal(nodeBinaryURL('darwin', 'arm64'), '/api/studio/nodes/binary/darwin/arm64?expectedAccount=account-a');
    setApiAccount('account-b');
    await api.bots();
    assert.equal(calls.at(-1).options.headers.get('X-Connect-Bots-Node'), 'local', 'another account cannot inherit the previous account node');
  } finally {
    setApiAccount(null);
    globalThis.fetch = original;
  }
});

test('aggregate catalogs bind an explicit account and host without changing the active chat', async () => {
  const original = globalThis.fetch;
  const calls = [];
  const controller = new AbortController();
  setApiAccount('account-b');
  setApiNode('active-mac-b');
  globalThis.fetch = async (path, options) => {
    calls.push({ path, options });
    return new Response(JSON.stringify({ bots: [], nodes: [] }), { status: 200 });
  };
  try {
    await api.bots('other-mac-a', controller.signal, 'account-a');
    await api.nodes('account-a');
    await api.bots();
    assert.equal(calls[0].options.headers.get('X-Connect-Bots-Account'), 'account-a');
    assert.equal(calls[0].options.headers.get('X-Connect-Bots-Node'), 'other-mac-a');
    assert.equal(calls[0].options.signal, controller.signal);
    assert.equal(calls[1].options.headers.get('X-Connect-Bots-Account'), 'account-a');
    assert.equal(calls[1].options.headers.has('X-Connect-Bots-Node'), false);
    assert.equal(calls[2].options.headers.get('X-Connect-Bots-Account'), 'account-b');
    assert.equal(calls[2].options.headers.get('X-Connect-Bots-Node'), 'active-mac-b');
  } finally {
    setApiAccount(null);
    globalThis.fetch = original;
  }
});

test('native clear response preserves its baseline for newer goal replay', async () => {
  const original = globalThis.fetch;
  const calls = [];
  setApiAccount('user-1');
  globalThis.fetch = async (path, options) => {
    calls.push({ path, method: options.method });
    return new Response(JSON.stringify({ cleared: true, cursor: 19 }), { status: 200 });
  };
  try {
    assert.deepEqual(await api.clearGoal('my/bot'), { goal: null, cursor: 19 });
    assert.deepEqual(calls, [{ path: '/api/studio/bots/my%2Fbot/goal', method: 'DELETE' }]);
  } finally {
    setApiAccount(null);
    globalThis.fetch = original;
  }
});

test('an invalid clear response cannot silently erase a pinned goal', async () => {
  const original = globalThis.fetch;
  setApiAccount('user-1');
  globalThis.fetch = async () => new Response(JSON.stringify({ cursor: 19 }), { status: 200 });
  try {
    await assert.rejects(api.clearGoal('bot'), /unexpected response/);
  } finally {
    setApiAccount(null);
    globalThis.fetch = original;
  }
});
