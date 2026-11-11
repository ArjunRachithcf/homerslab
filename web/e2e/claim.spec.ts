// The one-time link's claim, when the network or the manager drops it, or drops its answer: the link still works, so
// the page sends the claim again with the tab's nonce, and keeps the key and the nonce in the address bar until the
// manager has answered. The tab stores nothing but its session token (#101).
import type { Page, Request, Route } from '@playwright/test';
import { expect, test } from './primary';

const CLAIM = '**/api/v1/setup/claim';
const welcome = 'Let’s set up HomersLab';

/** The address of a link whose claim is still out: its key, and the tab's nonce. */
const stillOut = (localUrl: string) => new RegExp(`^${localUrl.replace(/[.*+?^$|()[\]{}\\/]/g, '\\$&')}&n=[A-Za-z0-9_-]{43}$`);

/** What a claim carried. */
const sentWith = (r: Request) => r.postDataJSON() as { key: string; nonce: string };

/** The tab's storage: only the session token, once there is one. */
const stored = (page: Page) => page.evaluate(() => [...Object.keys(sessionStorage), ...Object.keys(localStorage)]);

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

test('a reload while the claim is on its way to the manager still opens setup', async ({ page, primary }) => {
  const link = await primary.newLink();
  let held: Route | undefined;
  await page.route(CLAIM, async (route) => {
    if (!held) held = route; // the claim hasn't reached the manager yet: the browser is reloaded first
    else await route.continue();
  });
  await page.goto(link.localUrl);
  await expect.poll(() => held).toBeDefined();
  await expect(page).toHaveURL(stillOut(link.localUrl)); // not answered yet: the key is still in the address bar

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
  await expect(page).toHaveURL(stillOut(link.localUrl)); // the link still works, so it stays

  await page.unroute(CLAIM);
  await page.getByRole('button', { name: 'Try again' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  await expect(page).toHaveURL(/\/setup#\/welcome$/);
});

test('a claim whose answer was lost is sent again, and opens setup with one session', async ({ page, primary }) => {
  const link = await primary.newLink();
  const claims: Request[] = [];
  let first = true;
  await page.route(CLAIM, async (route) => {
    claims.push(route.request());
    if (first) {
      first = false;
      await route.fetch(); // the manager takes the claim…
      await route.abort('connectionreset'); // …and its answer is lost on the way back
    } else {
      await route.continue();
    }
  });
  await page.goto(link.localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  await expect(page).toHaveURL(/\/setup#\/welcome$/);
  expect(claims.length).toBe(2);
  const [sent, again] = claims.map(sentWith);
  expect(again).toEqual(sent); // the same key, the same nonce
  expect(primary.openSessions()).toBe(1);
  expect(primary.log()).toContain('sent again by the tab that claimed it');
  await page.getByRole('button', { name: 'Get started' }).click(); // the session the second answer gave works
  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');
});

test('a reload after the manager took the claim, before its answer came, opens setup with one session', async ({ page, primary }) => {
  const link = await primary.newLink();
  const claims: Request[] = [];
  let taken = false;
  await page.route(CLAIM, async (route) => {
    claims.push(route.request());
    if (taken) return route.continue();
    await route.fetch(); // the manager takes the claim; its answer never reaches this page
    taken = true;
  });
  await page.goto(link.localUrl);
  await expect.poll(() => taken).toBe(true);
  await expect(page).toHaveURL(stillOut(link.localUrl));

  await page.reload();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  expect(claims.length).toBe(2);
  const [sent, again] = claims.map(sentWith);
  expect(again).toEqual(sent);
  expect(primary.openSessions()).toBe(1);
  await page.getByRole('button', { name: 'Get started' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');
});

test('a claim sent again with another tab’s nonce is refused', async ({ browser, primary }) => {
  const link = await primary.newLink();
  const first = await browser.newPage();
  let taken = false;
  await first.route(CLAIM, async (route) => {
    await route.fetch(); // the first tab's claim is taken, and its answer lost
    taken = true;
  });
  await first.goto(link.localUrl);
  await expect.poll(() => taken).toBe(true);

  const second = await (await browser.newContext()).newPage(); // the same machine, the same link, its own nonce
  await second.goto(link.localUrl);
  await expect(second.getByRole('heading', { level: 1 })).toContainText('This link doesn’t work any more');
  expect(primary.openSessions()).toBe(1);
});

test('the tab stores nothing but its session token, even while its claim is out', async ({ page, primary }) => {
  const link = await primary.newLink();
  let held: Route | undefined;
  await page.route(CLAIM, (route) => { held = route; });
  await page.goto(link.localUrl);
  await expect.poll(() => held).toBeDefined();
  expect(await stored(page)).toEqual([]); // neither the key nor the nonce

  await held?.continue();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  expect(await stored(page)).toEqual(['homerslab.setup.session']);
});

test('a reload after the claim was answered keeps the session, and stores nothing more', async ({ page, primary }) => {
  const link = await primary.newLink();
  await page.goto(link.localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  await page.reload();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  expect(await stored(page)).toEqual(['homerslab.setup.session']);
  await expect(page).toHaveURL(/\/setup#\/welcome$/);
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
  expect(await stored(second)).toEqual([]);
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
  for (const c of claims) expect(JSON.parse(c.body)).toEqual({ key, nonce: expect.stringMatching(/^[A-Za-z0-9_-]{43}$/) });
  const nonce = (JSON.parse(claims[0]?.body ?? '{}') as { nonce: string }).nonce;
  for (const r of sent) expect(r.headers).not.toContain(nonce);
  for (const r of sent.filter((r) => !r.url.endsWith('/api/v1/setup/claim'))) expect(r.url + r.body).not.toContain(nonce);
  expect(primary.log()).not.toContain(nonce);
  for (const r of sent) {
    expect(r.url).not.toContain(key);
    expect(r.headers).not.toContain(key);
  }
  for (const r of sent.filter((r) => !r.url.endsWith('/api/v1/setup/claim'))) expect(r.body).not.toContain(key);
  expect(primary.log()).toContain('setup link claimed'); // the log is read
  expect(primary.log()).not.toContain(key);
});

test('a claim the manager refuses from here says so, and keeps the link', async ({ page, primary }) => {
  const link = await primary.newLink();
  await page.route(CLAIM, (route) =>
    route.fulfill({ status: 403, contentType: 'application/json', body: '{"error":"not-from-lan"}' }));
  await page.goto(link.localUrl);
  await expect(page.getByText('HomersLab takes setup only from this machine or your home network')).toBeVisible();
  await expect(page.getByText('didn’t answer')).toHaveCount(0); // it did answer
  await expect(page).toHaveURL(stillOut(link.localUrl));
});

test('a late answer to an earlier link’s claim doesn’t replace the newer link’s session', async ({ page, primary }) => {
  const first = await primary.newLink();
  let late: (() => Promise<void>) | undefined;
  await page.route(CLAIM, async (route) => {
    if (late) return route.continue();
    const answer = await route.fetch(); // the manager takes the first link…
    late = () => route.fulfill({ response: answer }); // …and its answer is held up on the way back
  });
  await page.goto(first.localUrl);
  await expect.poll(() => late).toBeDefined();

  await page.goto((await primary.newLink()).localUrl); // a newer link, in the same tab: it ends the first one's session
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  await late?.();
  await page.getByRole('button', { name: 'Get started' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');
});

test('a late answer from before a link arrived doesn’t replace setup', async ({ page, primary }) => {
  const link = await primary.newLink();
  let late: Route | undefined;
  await page.route('**/api/v1/setup', async (route) => {
    if (late) return route.continue();
    late = route; // the page without a link asks for setup, and the answer (no session) is held up
  });
  await page.goto(link.localUrl.replace(/#.*/, ''));
  await expect.poll(() => late).toBeDefined();
  await page.goto(link.localUrl); // then the link arrives in the same tab
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  await late?.continue();
  await page.waitForTimeout(500);
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
});

// A full disk under the manager's database (#70): the claim is answered 507, the page says the disk is full, the
// link still works so its key stays, and "Try again" claims it once there is room.
test('a claim the manager answers "disk full" says so, keeps the key, and "Try again" claims it', async ({ page, primary }) => {
  const link = await primary.newLink();
  await page.route(CLAIM, (route) =>
    route.fulfill({ status: 507, contentType: 'application/json', body: '{"error":"disk-full"}' }));
  await page.goto(link.localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('This machine’s disk is full');
  await expect(page.getByText('Setup can’t reach HomersLab')).toHaveCount(0);
  await expect(page).toHaveURL(stillOut(link.localUrl)); // the link still works, so it stays

  await page.unroute(CLAIM);
  await page.getByRole('button', { name: 'Try again' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(welcome);
  await expect(page).toHaveURL(/\/setup#\/welcome$/);
});
