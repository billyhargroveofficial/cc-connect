import assert from 'node:assert/strict';
import test from 'node:test';
import {
  parseThemePreference,
  readThemePreference,
  resolveTheme,
  saveThemePreference,
  subscribeSystemTheme,
  themeStorageKey,
} from './theme.ts';

test('theme preference preserves explicit modes and safely defaults to system', () => {
  for (const value of ['system', 'light', 'dark']) {
    assert.equal(parseThemePreference(value), value);
    assert.equal(readThemePreference({ getItem: () => value }), value);
  }
  assert.equal(parseThemePreference(null), 'system');
  assert.equal(parseThemePreference('unknown'), 'system');
  assert.equal(readThemePreference({ getItem() { throw new Error('blocked'); } }), 'system');
});

test('system resolves live device color while explicit choices ignore it', () => {
  assert.equal(resolveTheme('system', false), 'light');
  assert.equal(resolveTheme('system', true), 'dark');
  assert.equal(resolveTheme('light', true), 'light');
  assert.equal(resolveTheme('dark', false), 'dark');
});

test('selected preference is stored instead of the resolved system color', () => {
  const values = [];
  saveThemePreference({ setItem: (key, value) => values.push([key, value]) }, 'system');
  assert.deepEqual(values, [[themeStorageKey, 'system']]);
  assert.doesNotThrow(() => saveThemePreference({ setItem() { throw new Error('blocked'); } }, 'dark'));
});

test('system theme subscription reports changes and removes its exact listener', () => {
  const listeners = new Set();
  const media = {
    matches: false,
    addEventListener: (type, listener) => { assert.equal(type, 'change'); listeners.add(listener); },
    removeEventListener: (type, listener) => { assert.equal(type, 'change'); listeners.delete(listener); },
  };
  const values = [];
  const dispose = subscribeSystemTheme(media, value => values.push(value));
  assert.deepEqual(values, [false]);
  assert.equal(listeners.size, 1);
  media.matches = true;
  for (const listener of listeners) listener({ matches: true });
  assert.deepEqual(values, [false, true]);
  dispose();
  assert.equal(listeners.size, 0);
  for (const listener of listeners) listener({ matches: false });
  assert.deepEqual(values, [false, true]);
});

test('legacy MediaQueryList listener has matched cleanup', () => {
  const listeners = new Set();
  const media = {
    matches: true,
    addListener: listener => listeners.add(listener),
    removeListener: listener => listeners.delete(listener),
  };
  const values = [];
  const dispose = subscribeSystemTheme(media, value => values.push(value));
  assert.deepEqual(values, [true]);
  assert.equal(listeners.size, 1);
  dispose();
  assert.equal(listeners.size, 0);
});
