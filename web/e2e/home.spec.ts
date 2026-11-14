// Home in a real browser (#21), scripted from the Home prototype (prototypes/assets/home.js): a person signs in with the
// household account, sees one tile per app they may open with its health, and opens an app from it. Home installs on
// a phone as a PWA, with the logo's icons.
import { apps, expect, ines, test, tomas } from './home';

test('Rui signs in with the household account, sees his apps with their health, and opens one from Home', async ({ page, home }) => {
  await page.goto(home.url('home'));
  await expect(page).toHaveURL(home.url('home')); // to the sign-in and back
  const h1 = page.getByRole('heading', { level: 1 });
  await expect(h1).toContainText('Rui.');
  await expect(h1).toContainText('Donetick isn’t working.');
  await expect(page.getByText('The rest are working.')).toBeVisible();

  // One tile per app his audience may open: the admins' terminal isn't one of them.
  const tiles = page.getByRole('main').getByRole('link').filter({ has: page.locator('.app-icon') });
  await expect(tiles).toHaveCount(3);
  await expect(page.getByRole('link', { name: /Termix/ })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'BentoPDF PDF tools Working' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Donetick Chores Not working' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Photos, documents and links 2' })).toBeVisible();
  const icon = page.getByRole('link', { name: /BentoPDF/ }).locator('img');
  await expect(icon).toHaveJSProperty('complete', true);
  expect(await icon.evaluate((img: HTMLImageElement) => img.naturalWidth)).toBeGreaterThan(0);
  await expect(page.getByRole('heading', { name: 'For you as admin' })).toHaveCount(0);

  // A tile opens its app, at the app's own name.
  await page.getByRole('link', { name: /BentoPDF/ }).click();
  await expect(page).toHaveURL(home.url('pdf'));
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('BentoPDF');
});

test('what Home shows follows the projection: an app’s health, and one installed since', async ({ page, home }) => {
  await page.goto(home.url('home'));
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Donetick isn’t working.');
  await home.write([...apps.map((a) => ({ ...a, health: 'healthy' })),
    { id: 'jellyfin', name: 'Jellyfin', purpose: 'Films and series', category: 'films', host: 'films', audience: ['admin', 'adult', 'kid'], health: 'starting' }]);
  await page.reload();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Jellyfin is starting.');
  await expect(page.getByRole('link', { name: 'Donetick Chores Working' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Jellyfin Films and series Starting' })).toBeVisible();
});

test('a kid’s Home has their own tiles, of the apps for kids only', async ({ page, home }) => {
  home.person = tomas;
  await page.goto(home.url('home'));
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Hi, Tomás. What would you like to do?');
  const tiles = page.locator('.kid-tile');
  await expect(tiles).toHaveCount(1);
  await expect(page.getByRole('link', { name: 'Donetick Chores Not working right now' })).toBeVisible();
  await page.getByRole('link', { name: /Donetick/ }).click();
  await expect(page).toHaveURL(home.url('chores'));
});

test('an admin’s Home has every app, and Admin', async ({ page, home }) => {
  home.person = ines;
  await page.goto(home.url('home'));
  await expect(page.getByRole('link', { name: 'Termix A terminal in the browser Starting' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'More apps 1' })).toBeVisible();
  await expect(page.getByRole('link', { name: /^Admin/ })).toHaveAttribute('href', home.url('admin', ''));
});

test('signing out from the person’s menu closes Home on this device', async ({ page, home }) => {
  await page.goto(home.url('home'));
  await page.getByRole('button', { name: 'Rui Ferreira' }).click();
  const menu = page.getByRole('dialog', { name: 'Your account' });
  await expect(menu).toBeVisible();
  await expect(menu.getByText('Adult in the Ferreira household')).toBeVisible();
  await menu.getByRole('button', { name: /Sign out/ }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('You’re signed out');
  expect((await page.context().cookies()).filter((c) => c.name === '__Host-homerslab-home')).toEqual([]);
  const api = await page.evaluate(async () => (await fetch('/api/v1/home')).status);
  expect(api).toBe(401);
});

test('Home installs on a phone as a PWA, with the logo’s icons', async ({ page, home, browserName }) => {
  test.skip(browserName !== 'chromium', 'installability comes from Chromium’s DevTools protocol');
  await page.goto(home.url('home'));
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Rui.');
  const manifest = await page.evaluate(async () => {
    const link = document.querySelector<HTMLLinkElement>('link[rel="manifest"]');
    const r = await fetch(link?.href ?? '', { credentials: 'omit' }); // as a browser fetches a manifest: no cookies
    return { type: r.headers.get('Content-Type'), body: await r.json() as { name: string; short_name: string; display: string; start_url: string; icons: { src: string; sizes: string; purpose?: string }[] } };
  });
  expect(manifest.type).toBe('application/manifest+json');
  expect(manifest.body).toMatchObject({ name: 'Home · HomersLab', short_name: 'Home', display: 'standalone', start_url: '/' });
  expect(manifest.body.icons.map((i) => `${i.sizes}${i.purpose ? ` ${i.purpose}` : ''}`)).toEqual(['192x192', '512x512', '512x512 maskable', 'any']);
  for (const i of manifest.body.icons) {
    const got = await page.evaluate(async (src) => {
      const r = await fetch(src, { credentials: 'omit' });
      return `${r.status} ${r.headers.get('Content-Type')}`;
    }, i.src);
    expect(got, i.src).toBe(`200 ${i.src.endsWith('.svg') ? 'image/svg+xml' : 'image/png'}`);
  }
  const cdp = await page.context().newCDPSession(page);
  const { installabilityErrors } = await cdp.send('Page.getInstallabilityErrors');
  expect(installabilityErrors).toEqual([]);
});
