import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import * as react from 'react';
import * as jsxRuntime from 'react/jsx-runtime';
import { renderToStaticMarkup } from 'react-dom/server';
import * as markdown from 'react-markdown';
import * as gfm from 'remark-gfm';
import * as math from 'remark-math';
import * as highlight from 'rehype-highlight';
import * as katex from 'rehype-katex';

// Use the production component and its real Markdown/KaTeX pipeline. The
// unrelated transcript controls do not need a DOM for these rendering tests.
const source = ts.transpileModule(readFileSync(new URL('./Transcript.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const modules = {
  react, 'react/jsx-runtime': jsxRuntime,
  'react-markdown': markdown, 'remark-gfm': gfm, 'remark-math': math,
  'rehype-highlight': highlight, 'rehype-katex': katex,
  'lucide-react': { ArrowUpRight: () => null, Copy: () => null },
  '../../lib/api': { accountURL: value => value },
  '../../components/Avatar': {}, '../../lib/events': {},
  '../../lib/motion': {
    m: {
      button: 'button',
      span: ({ children }) => react.createElement('span', null, children),
    },
    AnimatePresence: ({ children }) => children,
    motionTransition: {},
  },
  './reducer': {}, './transcript.css': {}, 'katex/dist/katex.min.css': {},
};
const exports = {};
runInNewContext(source, {
  exports,
  require: name => {
    assert.ok(name in modules, `Unexpected transcript import: ${name}`);
    return modules[name];
  },
}, { filename: 'Transcript.tsx' });

const render = content => renderToStaticMarkup(react.createElement(exports.Markdown, { content }));
const mathCount = html => (html.match(/class="katex"/g) || []).length;

test('currency dollars in Russian subscription prose keep their words, spaces, and Markdown', () => {
  const content = 'OpenAI урезала $200-подписку. **Контекст:** план за $200 — лимиты Work/Codex упали с 20× до 10×; '
    + 'цена не изменилась. Добавили Pro 500 за $500/мес. и 2 500 кредитов. **Оговорки** остаются обычным текстом.';
  const html = render(content);
  assert.equal(mathCount(html), 0);
  assert.ok(html.includes(content.replaceAll('**Контекст:**', '<strong>Контекст:</strong>').replaceAll('**Оговорки**', '<strong>Оговорки</strong>')));
});

test('currency next to inline math and formatting cannot absorb a later formula', () => {
  const html = render(String.raw`План стоит $200, год — $2,400.00; новый — $500/мес. Результат: **$0.5 \times 14.5 \approx 7.25$**. Цена $20 в месяц; затем **$x+1$**.`);
  assert.equal(mathCount(html), 2);
  for (const text of ['$200, год — $2,400.00', '$500/мес. Результат:', 'Цена $20 в месяц; затем']) assert.ok(html.includes(text), html);
  assert.ok(html.includes('<strong><span class="katex">'));
});

test('currency in Markdown emphasis, strikethrough, and tables does not consume adjacent math', () => {
  const html = render('**$200** → **$x$**, ~~$500~~ → ~~$y$~~, *$20* → *$z$*.\n\n'
    + '| Цена | Формула |\n| --- | --- |\n| $200 | $x+1$ |\n| $500 | $2x$ |');
  assert.equal(mathCount(html), 5);
  for (const literal of ['<strong>$200</strong>', '<del>$500</del>', '<em>$20</em>', '<td>$200</td>', '<td>$500</td>']) assert.ok(html.includes(literal), html);
});

test('single-dollar numeric and symbolic formulas, inline double dollars, and display math still render', () => {
  const html = render(String.raw`Число $200$, расчёт $2+2=4$, дробь $\frac{1}{2}$, переменная $x_i$, произведения $2x$ и $2 xy$, выражения $2-xy$ и $2/xy$, текст $2 \text{ рубля}$ и $$a^2+b^2=c^2$$.

$$
E = mc^2
$$`);
  assert.equal(mathCount(html), 11);
  assert.equal((html.match(/class="katex-display"/g) || []).length, 1);
});

test('escaped dollar signs and dollars in code and links remain literal', () => {
  const html = render(String.raw`Цена \$200, литерал \$x\$ и формула $x$; код \`$200 и $500\`.

\`\`\`text
$200 и $500
\`\`\`

[Тариф $200](https://example.com/plans/$200) и [ещё $500](https://example.com/plans/$500).`.replaceAll('\\`', '`'));
  assert.equal(mathCount(html), 1);
  assert.ok(html.includes('Цена $200, литерал $x$ и формула'));
  assert.ok(html.includes('<code>$200 и $500</code>'));
  assert.ok(html.includes('href="https://example.com/plans/$200"'));
  assert.ok(html.includes('>Тариф $200</a>'));
});
