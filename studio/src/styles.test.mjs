import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import postcss from 'postcss';

const styles = postcss.parse(readFileSync(new URL('./styles.css', import.meta.url), 'utf8'));
const island = postcss.parse(readFileSync(new URL('./features/chat/bot-island.css', import.meta.url), 'utf8'));
const composer = postcss.parse(readFileSync(new URL('./features/chat/minimal-composer.css', import.meta.url), 'utf8'));

function declarations(css, selector) {
  const values = new Map();
  css.walkRules(rule => {
    if (rule.parent.type !== 'root' || !rule.selectors.includes(selector)) return;
    rule.walkDecls(declaration => values.set(declaration.prop, declaration.value));
  });
  return values;
}

test('the shell, composer, hosts and bot island use borderless surfaces', () => {
  const visibleBorders = [];
  for (const css of [styles, island]) {
    css.walkDecls(declaration => {
      if (!/^border(?:-(?:top|right|bottom|left))?(?:-width)?$/.test(declaration.prop)) return;
      if (/^(?:none|0(?:px)?)(?:\s*!important)?$/.test(declaration.value)) return;
      visibleBorders.push(`${declaration.parent.selector}: ${declaration.prop}: ${declaration.value}`);
    });
  }
  assert.deepEqual(visibleBorders, [], 'resting UI surfaces must not draw dividing or enclosing lines');
  assert.equal(declarations(styles, '.roster').get('background'), 'var(--sidebar-bg)');
  assert.equal(declarations(island, '.bot-island-card').get('background'), 'var(--surface-raised)');
  assert.ok(declarations(island, '.bot-island-card').get('box-shadow'), 'the island remains legible without a border');
});

test('borderless controls preserve keyboard focus and invalid-field feedback', () => {
  assert.equal(declarations(styles, 'button:focus-visible').get('outline'), '2px solid var(--accent)');
  assert.equal(declarations(styles, 'input:focus-visible').get('outline'), '2px solid var(--accent)');
  assert.equal(declarations(styles, '.login-card input:focus-visible').get('outline'), '2px solid var(--accent)');
  assert.equal(declarations(styles, '.composer:has(textarea:focus-visible)').get('outline'), '2px solid var(--accent)');
  assert.ok(declarations(styles, '.login-card input[aria-invalid="true"]').get('background'),
    'invalid fields retain a visible error surface without restoring a border');
  assert.notEqual(declarations(styles, '.cb-host-pair-code input').get('outline'), 'none',
    'the pairing-code field must not override the global focus-visible outline');
});

test('goal modal keeps a fixed scrim without backdrop blur in either theme', () => {
  const scrim = declarations(styles, '.dialog-layer');
  assert.equal(scrim.get('position'), 'fixed');
  assert.equal(scrim.get('inset'), '0');
  assert.equal(scrim.get('backdrop-filter'), 'none');
  assert.equal(scrim.get('background'), 'var(--scrim)');
  for (const theme of [':root', ':root[data-theme="light"]']) {
    const tokens = declarations(styles, theme);
    assert.ok(tokens.get('--scrim'));
    assert.ok(tokens.get('--shadow-soft'));
    assert.ok(tokens.get('--shadow-popover'));
    assert.ok(tokens.get('--surface-control'));
  }
});

test('the animated roster status dot is optically centered with its label', () => {
  const preview = declarations(styles, '.bot-row-preview');
  const pip = declarations(styles, '.live-pip');
  const label = declarations(styles, '.bot-row-preview-label');
  assert.equal(preview.get('display'), 'flex');
  assert.equal(preview.get('align-items'), 'center');
  assert.equal(pip.get('display'), 'inline-block');
  assert.equal(label.get('text-overflow'), 'ellipsis',
    'centering the status dot must preserve truncation for long status labels');
});

test('the effort heading shares one continuous popover surface', () => {
  assert.equal(declarations(composer, '.model-select-current').get('background'), 'transparent',
    'the selected model must not draw a second rectangle inside the effort popover');
});

test('the composer model trigger keeps its background and color unchanged on hover in both themes', () => {
  for (const [css, selector] of [[styles, '.model-trigger'], [composer, '.composer-minimal .model-trigger']]) {
    const resting = declarations(css, selector);
    const hovered = declarations(css, `${selector}:hover:not(:disabled)`);
    assert.equal(resting.get('background'), 'transparent');
    assert.equal(hovered.get('background'), 'transparent', 'hover cannot introduce a filled rectangle');
    assert.equal(hovered.get('color'), resting.get('color'), 'hover cannot recolor the model text');
  }
  for (const theme of [':root', ':root[data-theme="light"]']) {
    assert.ok(declarations(styles, theme).get('--text'), 'the steady text color comes from each theme');
  }
});

test('the Fast lightning keeps a full touch target and no hover fill or recolor', () => {
  const resting = declarations(composer, '.inference-heading > .inference-fast-toggle');
  const hovered = declarations(composer, '.inference-heading > .inference-fast-toggle:hover');
  const active = declarations(composer, '.inference-heading > .inference-fast-toggle[aria-pressed="true"]');
  const activeHovered = declarations(composer, '.inference-heading > .inference-fast-toggle[aria-pressed="true"]:hover');
  assert.equal(resting.get('width'), '44px');
  assert.equal(resting.get('height'), '44px');
  assert.equal(resting.get('min-width'), '44px');
  assert.equal(resting.get('min-height'), '44px');
  assert.equal(resting.get('background'), 'transparent');
  assert.equal(hovered.get('background'), 'transparent');
  assert.equal(hovered.get('color'), resting.get('color'));
  assert.equal(active.get('color'), 'var(--accent)');
  assert.equal(activeHovered.get('color'), active.get('color'));
});
