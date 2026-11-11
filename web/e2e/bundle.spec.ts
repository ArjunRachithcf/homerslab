// The pages carry our code and React's, and none of vite's own (#115): one entry and no dynamic imports, so vite's
// modulepreload polyfill and its preload helper have nothing to do (vite.config.ts turns the polyfill off).
import { expect, test } from './primary';

test('the page loads no code of vite’s own', async ({ page, primary }) => {
  const scripts: Promise<string>[] = [];
  page.on('response', (r) => {
    if (r.request().resourceType() === 'script') scripts.push(r.text());
  });
  await page.goto((await primary.newLink()).localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');

  const bodies = await Promise.all(scripts);
  expect(bodies).toHaveLength(1); // the one entry: no chunks, nothing loaded later
  for (const body of bodies) {
    // React DOM names rel="modulepreload" too (its preloadModule), so this looks for the polyfill's own feature test.
    expect(/supports\(\s*[`'"]modulepreload[`'"]\s*\)/.test(body), 'vite’s modulepreload polyfill is in the bundle').toBe(false);
    expect(body.includes('vite:preloadError'), 'vite’s preload helper (dynamic imports) is in the bundle').toBe(false);
  }
});
