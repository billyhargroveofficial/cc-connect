import assert from 'node:assert/strict';
import test from 'node:test';
import { avatarStyles, resolveAvatarStyle } from './avatars.ts';

test('avatar styles keep six distinct selectable characters', () => {
  assert.deepEqual(avatarStyles.map((style) => style.id), [
    'lavender', 'mint', 'peach', 'blue', 'rose', 'amber',
  ]);
  assert.equal(new Set(avatarStyles.map((style) => style.variant)).size, avatarStyles.length);
  for (const style of avatarStyles) {
    assert.match(style.base, /^#[0-9a-f]{6}$/i);
    assert.match(style.light, /^#[0-9a-f]{6}$/i);
    assert.match(style.shade, /^#[0-9a-f]{6}$/i);
    assert.match(style.ink, /^#[0-9a-f]{6}$/i);
  }
});

test('legacy colors and unknown avatars resolve to a stable current style', () => {
  assert.equal(resolveAvatarStyle('purple', 'bot-one').id, 'lavender');
  assert.equal(resolveAvatarStyle('green', 'bot-one').id, 'mint');
  assert.equal(resolveAvatarStyle('orange', 'bot-one').id, 'peach');
  const first = resolveAvatarStyle('custom', 'persistent-bot');
  const second = resolveAvatarStyle('custom', 'persistent-bot');
  assert.equal(first.id, second.id);
  assert.ok(avatarStyles.includes(first));
});
