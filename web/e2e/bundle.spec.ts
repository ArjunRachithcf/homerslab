// The pages carry our code and React's, and none of vite's own (#115): no dynamic imports, so vite's modulepreload
// polyfill and its preload helper have nothing to do (vite.config.ts turns the polyfill off). Each page is its entry
// and the one chunk it shares with the other page, which its entry imports from its first line.
import type { Page } from '@playwright/test';
import { expect, test } from './primary';

async function loads(page: Page, open: () => Promise<void>): Promise<string[]> {
  const scripts: Promise<string>[] = [];
  page.on('response', (r) => {
    if (r.request().resourceType() === 'script' && r.status() === 200) scripts.push(r.text());
  });
  await open();
  await page.waitForLoadState('networkidle');
  return Promise.all(scripts);
}

for (const surface of ['setup', 'Admin'] as const) test(`${surface}’s page loads no code of vite’s own`, async ({ page, primary }) => {
  const bodies = await loads(page, async () => {
    if (surface === 'setup') {
      await page.goto((await primary.newLink()).localUrl);
      await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
    } else {
      primary.withDomain();
      await page.goto(await primary.adminURL());
      await expect(page.getByRole('heading', { level: 1 })).toHaveText('Store');
    }
  });
  expect(bodies).toHaveLength(2); // the entry and the shared chunk: nothing loaded later
  const shared = /^import\{[^}]*\}from"\.\/([\w-]+\.js)";/.exec(bodies[0] ?? '')?.[1];
  expect(shared, 'the entry imports the shared chunk statically, first').toBeTruthy();
  for (const body of bodies) {
    // React DOM names rel="modulepreload" too (its preloadModule), so this looks for the polyfill's own feature test.
    expect(/supports\(\s*[`'"]modulepreload[`'"]\s*\)/.test(body), 'vite’s modulepreload polyfill is in the bundle').toBe(false);
    // The preload helper of dynamic imports: its name (when not minified) and the event it fires.
    expect(body.includes('__vitePreload'), 'vite’s preload helper is in the bundle').toBe(false);
    expect(body.includes('vite:preloadError'), 'vite’s preload helper is in the bundle').toBe(false);
  }
});
