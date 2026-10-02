import assert from 'node:assert/strict';
import test from 'node:test';
import { api, accountURL, fileURL, onApiAccountChanged, setApiAccount } from './api.ts';

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
  try {
    assert.equal(fileURL('bot', { id: 'file', name: 'test.png', mimeType: 'image/png' }), '/api/studio/bots/bot/files/file?expectedAccount=account%2Fa');
    assert.equal(fileURL('bot', { id: 'file', url: '/api/studio/bots/bot/files/file?download=1#preview' }), '/api/studio/bots/bot/files/file?download=1&expectedAccount=account%2Fa#preview');
    assert.equal(accountURL('https://example.com/api/studio/files/photo.png'), 'https://example.com/api/studio/files/photo.png');
    assert.equal(accountURL('/assets/logo.svg'), '/assets/logo.svg');
  } finally { setApiAccount(null); }
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
