import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';
import { motionTestModule } from '../lib/motion-stub.mjs';

const source = ts.transpileModule(`${readFileSync(new URL('./Login.tsx', import.meta.url), 'utf8')}\nexport { LoginNotice };`, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

function notice(reduced) {
  const element = (type, props) => ({ type, props });
  const modules = {
    react: {},
    'react/jsx-runtime': { jsx: element, jsxs: element },
    'lucide-react': new Proxy({}, { get: (_, name) => name }),
    './Brand': { default: 'Brand' },
    './Avatar': { default: 'Avatar' },
    '../lib/api': {},
    '../lib/motion': {
      ...motionTestModule(),
      useReducedMotion: () => reduced,
      motionTransition: { disclosure: { duration: 0.19 } },
    },
  };
  const exports = {};
  runInNewContext(source, { exports, require: name => modules[name] }, { filename: 'Login.tsx' });
  return exports.LoginNotice({ message: 'Unable to connect.' });
}

test('login notices reveal immediately when reduced motion is requested', () => {
  assert.equal(notice(false).props.transition.duration, 0.19);
  assert.equal(notice(true).props.transition.duration, 0, 'height animation must respect reduced motion');
});
