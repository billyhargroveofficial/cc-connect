import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const css = readFileSync(new URL('./settings.css', import.meta.url), 'utf8');

test('shared Settings keeps the workspace sharp and uses a larger desktop modal with a full-height mobile fallback', () => {
  assert.doesNotMatch(css, /backdrop-filter\s*:/, 'the Settings overlay must not blur the workspace');
  const wide = css.match(/\.cb-settings-panel--wide\s*\{([^}]+)\}/)?.[1];
  assert.match(wide, /width:\s*min\(960px,\s*calc\(100vw - 48px\)\)/);
  assert.match(wide, /height:\s*min\(850px,\s*calc\(100dvh - 64px\)\)/);
  const mobile = css.slice(css.indexOf('@media (max-width: 640px)'));
  assert.match(mobile, /\.cb-settings-panel\s*\{[^}]*width:\s*100%;[^}]*height:\s*100dvh;/);
});

test('Settings uses borderless surfaces while keeping keyboard focus and selected-tab affordances', () => {
  const declarations = [...css.matchAll(/(?:^|[;{])\s*border(?:-(?:top|right|bottom|left))?\s*:\s*([^;}]+)/g)].map(match => match[1].trim());
  assert.ok(declarations.length > 0);
  assert.ok(declarations.every(value => value === '0' || value === 'none'), 'decorative borders and separators must not return');
  assert.match(css, /\.cb-settings-field[^}]*:focus-visible[^}]*outline:\s*2px solid var\(--settings-accent\)/);
  assert.match(css, /\.cb-settings-tab-indicator\s*\{[^}]*background:\s*var\(--settings-raised\)/);
  assert.match(css, /\.cb-settings-overlay \.cb-host-entry\.is-active\s*\{[^}]*background:/);
});
