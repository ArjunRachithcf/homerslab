// The flow tests find the binary, the sysroot and the gallery from a checkout whose path has a space and a non-ASCII
// letter: paths.ts is copied into such a checkout (with the three files in their places) and loaded from there.
import { copyFile, mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { expect, test } from '@playwright/test';

test('the binary, the sysroot and the gallery are found in a checkout under "hl test ä"', async () => {
  const base = await mkdtemp(path.join(tmpdir(), 'homerslab-paths-'));
  try {
    const checkout = path.join(base, 'hl test ä');
    await mkdir(path.join(checkout, 'web', 'e2e'), { recursive: true });
    await mkdir(path.join(checkout, 'bin'));
    await mkdir(path.join(checkout, 'testdata', 'primary'), { recursive: true });
    await mkdir(path.join(checkout, 'design'));
    await writeFile(path.join(checkout, 'bin', 'homerslab'), '');
    await writeFile(path.join(checkout, 'design', 'index.html'), '');
    await copyFile(path.join(path.dirname(fileURLToPath(import.meta.url)), 'paths.ts'), path.join(checkout, 'web', 'e2e', 'paths.ts'));

    const env = { ...process.env };
    delete env.HOMERSLAB_BIN;
    // loaded by a node of its own: Playwright's loader can't import a second copy of a module
    const loader = `import(${JSON.stringify(pathToFileURL(path.join(checkout, 'web', 'e2e', 'paths.ts')).href)}).then((m) => console.log(JSON.stringify(m)))`;
    const { stdout } = await promisify(execFile)(process.execPath, ['--input-type=module', '-e', loader], { env });
    const found = JSON.parse(stdout) as { bin: string; sysroot: string; galleryUrl: string };
    expect(existsSync(found.bin), `the binary at ${found.bin}`).toBe(true);
    expect(existsSync(found.sysroot), `the sysroot at ${found.sysroot}`).toBe(true);
    expect(existsSync(fileURLToPath(found.galleryUrl)), `the gallery at ${found.galleryUrl}`).toBe(true);
    expect(new URL(found.galleryUrl).protocol).toBe('file:');
  } finally {
    await rm(base, { recursive: true, force: true });
  }
});
