import { cp, mkdir, readFile, readdir, rename, rm, writeFile } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// Retain hashed assets from prior builds so already-open tabs can still load
// their lazy chunks. Publish the entry point only after all new assets exist.
export async function publishBuild(source, target) {
  if (resolve(source) === resolve(target)) throw new Error('Publish to a directory separate from the build output.');
  const index = await readFile(join(source, 'index.html'));
  await mkdir(target, { recursive: true });
  for (const name of await readdir(source)) {
    if (name !== 'index.html') await cp(join(source, name), join(target, name), { recursive: true });
  }
  const temporary = join(target, `.index-${process.pid}-${Date.now()}.tmp`);
  try {
    await writeFile(temporary, index);
    await rename(temporary, join(target, 'index.html'));
  } finally { await rm(temporary, { force: true }); }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const target = process.argv[2];
  if (!target) throw new Error('Usage: pnpm deploy:local /absolute/path/to/live-studio');
  await publishBuild(fileURLToPath(new URL('../dist', import.meta.url)), resolve(target));
  console.log('Published Studio assets and entry point. Existing hashed assets retained.');
}
