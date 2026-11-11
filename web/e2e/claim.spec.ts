// The one-time link's claim, when the network or the manager drops it: the link still works, so the page tries it
// again, and keeps the key in the address bar until the manager has answered.
import type { Route } from '@playwright/test';
import { expect, test } from './primary';

const CLAIM = '**/api/v1/setup/claim';
const welcome = 'Let’s set up HomersLab';

test('a claim lost to the network once is tried again by itself', async ({ page, primary }) => {
  const link = await primary.newLink();
  let first = true;
  await page.route(CLAIM, async (route) => {
    if (first) {
      first = false;
      await route.abort('connectionreset'); // a Wi-Fi blip, or the manager restarting
    } else {
      await route.continue();
    }
  });
  await page.goto(link.localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  await expect(page).toHaveURL(/\/setup#\/welcome$/); // claimed: the key is gone from the address bar
});

test('a reload while the claim is under way still opens setup', async ({ page, primary }) => {
  const link = await primary.newLink();
  let held: Route | undefined;
  await page.route(CLAIM, async (route) => {
    if (!held) held = route; // the claim never reaches the manager: the browser is reloaded first
    else await route.continue();
  });
  await page.goto(link.localUrl);
  await expect.poll(() => held).toBeDefined();
  await expect(page).toHaveURL(link.localUrl); // not answered yet: the key is still in the address bar

  await page.reload();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  await expect(page).toHaveURL(/\/setup#\/welcome$/);
  await held?.abort().catch(() => undefined);
});

test('a claim that keeps getting no answer says so, and "Try again" claims the same link', async ({ page, primary }) => {
  test.slow(); // the page tries 4 times, over 7 seconds, before it says so
  const link = await primary.newLink();
  let tries = 0;
  await page.route(CLAIM, (route) => { tries++; return route.abort('connectionrefused'); });
  await page.goto(link.localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Setup can’t reach HomersLab', { timeout: 15_000 });
  expect(tries).toBe(4);
  await expect(page).toHaveURL(link.localUrl); // the link still works, so it stays

  await page.unroute(CLAIM);
  await page.getByRole('button', { name: 'Try again' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  await expect(page).toHaveURL(/\/setup#\/welcome$/);
});

test('the tab holds the key until its claim is answered, even when the address bar loses it', async ({ page, primary }) => {
  test.slow();
  const link = await primary.newLink();
  await page.route(CLAIM, (route) => route.abort('connectionrefused'));
  await page.goto(link.localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Setup can’t reach HomersLab', { timeout: 15_000 });

  await page.unroute(CLAIM);
  await page.goto(link.localUrl.replace(/#.*/, '')); // the same tab, the address without its fragment
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  expect(await page.evaluate(() => Object.keys(sessionStorage))).toEqual(['homerslab.setup.session']); // the key is let go
});

test('a link the manager refused leaves the address bar', async ({ browser, primary }) => {
  const link = await primary.newLink();
  const first = await browser.newPage();
  await first.goto(link.localUrl);
  await expect(first.getByRole('heading', { level: 1 })).toContainText(welcome);

  const second = await (await browser.newContext()).newPage();
  await second.goto(link.localUrl);
  await expect(second.getByRole('heading', { level: 1 })).toContainText('This link doesn’t work any more');
  await expect(second).toHaveURL(/\/setup#\/welcome$/);
  expect(await second.evaluate(() => Object.keys(sessionStorage))).toEqual([]);
});

test('the key goes only into the claim, however often it is sent', async ({ page, primary }) => {
  const link = await primary.newLink();
  const key = new URLSearchParams(new URL(link.localUrl).hash.slice(1)).get('k') ?? '';
  expect(key).not.toBe('');
  const sent: { url: string; headers: string; body: string }[] = [];
  page.on('request', (r) => sent.push({ url: r.url(), headers: JSON.stringify(r.headers()), body: r.postData() ?? '' }));
  let first = true;
  await page.route(CLAIM, async (route) => {
    if (first) {
      first = false;
      await route.abort('connectionreset');
    } else {
      await route.continue();
    }
  });
  await page.goto(link.localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);

  const claims = sent.filter((r) => r.url.endsWith('/api/v1/setup/claim'));
  expect(claims.length).toBe(2);
  for (const c of claims) expect(c.body).toContain(key);
  for (const r of sent) {
    expect(r.url).not.toContain(key);
    expect(r.headers).not.toContain(key);
  }
  for (const r of sent.filter((r) => !r.url.endsWith('/api/v1/setup/claim'))) expect(r.body).not.toContain(key);
  expect(primary.log()).toContain('setup link claimed'); // the log is read
  expect(primary.log()).not.toContain(key);
});
