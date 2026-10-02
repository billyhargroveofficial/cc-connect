import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { publishBuild } from './publish-local.mjs';

test('publishing preserves lazy chunks of open clients and replaces the index after a valid build', async () => {
  const root = await mkdtemp(join(tmpdir(), 'studio-publish-'));
  const build = join(root, 'build'), live = join(root, 'live');
  try {
    await mkdir(join(build, 'assets'), { recursive: true });
    await mkdir(join(live, 'assets'), { recursive: true });
    await writeFile(join(live, 'assets', 'old.js'), 'old chunk');
    await writeFile(join(live, 'index.html'), 'old index');
    await writeFile(join(build, 'assets', 'new.js'), 'new chunk');
    await assert.rejects(publishBuild(build, live));
    assert.equal(await readFile(join(live, 'index.html'), 'utf8'), 'old index');
    await writeFile(join(build, 'index.html'), 'new index');
    await publishBuild(build, live);
    assert.equal(await readFile(join(live, 'index.html'), 'utf8'), 'new index');
    assert.equal(await readFile(join(live, 'assets', 'old.js'), 'utf8'), 'old chunk');
    assert.equal(await readFile(join(live, 'assets', 'new.js'), 'utf8'), 'new chunk');
    await assert.rejects(publishBuild(build, build), /separate/);
  } finally { await rm(root, { recursive: true, force: true }); }
});
