// A step's answer the manager doesn't take: the page says why, in plain words, and keeps what the admin typed.
import type { Page } from '@playwright/test';
import { expect, test } from './primary';

const STEP = '**/api/v1/setup/steps/you';

/** Opens setup at "What's your name?", with the name and the mail typed in. */
async function atYou(page: Page, link: { localUrl: string }) {
  await page.goto(link.localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');
  await page.getByLabel('Your name', { exact: true }).fill('Inês Ferreira');
  await page.getByLabel('Your mail address', { exact: true }).fill('ines@ferreira.family');
}

async function kept(page: Page) {
  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');
  await expect(page.getByLabel('Your name', { exact: true })).toHaveValue('Inês Ferreira');
  await expect(page.getByLabel('Your mail address', { exact: true })).toHaveValue('ines@ferreira.family');
  await expect(page.getByRole('button', { name: 'Continue' })).toBeEnabled();
}

// The manager's answers to a step (internal/manager: putStep, and the listener's checks), each with what it says.
const failures: { name: string; status: number; error: string; says: string; reload?: boolean }[] = [
  { name: 'the manager fails to keep it', status: 500, error: 'internal', says: 'HomersLab couldn’t save this' },
  { name: 'the manager is busy', status: 503, error: 'unknown', says: 'HomersLab couldn’t save this' },
  { name: 'setup moved on elsewhere', status: 409, error: 'earlier-step-open', says: 'Setup moved on in another tab or browser', reload: true },
  { name: 'the home network’s half is done', status: 409, error: 'lan-half-done', says: 'carry on over the mesh', reload: true },
  { name: 'the step isn’t built yet', status: 501, error: 'not-built', says: 'This step comes with a later version of HomersLab' },
  { name: 'the step is answered over the mesh', status: 403, error: 'over-the-mesh', says: 'This step is answered over the mesh' },
  { name: 'it comes from outside the home network', status: 403, error: 'not-from-lan', says: 'only from this machine or your home network' },
  { name: 'it comes from another page', status: 403, error: 'cross-origin', says: 'only from this machine or your home network' },
  { name: 'the manager doesn’t know the step', status: 404, error: 'unknown-step', says: 'HomersLab didn’t take this answer', reload: true },
];

for (const f of failures) {
  test(`an answer refused because ${f.name} says so, and keeps the answer`, async ({ page, primary }) => {
    await atYou(page, await primary.newLink());
    await page.route(STEP, (route) =>
      route.fulfill({ status: f.status, contentType: 'application/json', body: JSON.stringify({ error: f.error }) }));
    await page.getByRole('button', { name: 'Continue' }).click();
    await expect(page.getByRole('alert')).toContainText(f.says);
    await expect(page.getByRole('alert').getByRole('button', { name: 'Reload' })).toHaveCount(f.reload ? 1 : 0);
    await kept(page);
  });
}

test('an answer that gets no answer says the device is offline, and keeps the answer', async ({ page, primary }) => {
  await atYou(page, await primary.newLink());
  await page.route(STEP, (route) => route.abort('internetdisconnected'));
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('alert')).toContainText('This device couldn’t reach HomersLab');
  await kept(page);
});

test('"Reload" after setup moved on shows where setup is now', async ({ page, primary }) => {
  await atYou(page, await primary.newLink());
  await page.route(STEP, (route) =>
    route.fulfill({ status: 409, contentType: 'application/json', body: '{"error":"earlier-step-open"}' }));
  await page.getByRole('button', { name: 'Continue' }).click();
  await page.getByRole('alert').getByRole('button', { name: 'Reload' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');
  await expect(page.getByRole('alert')).toHaveCount(0);
});

test('a failure that happens again is read out again, and one that is over goes', async ({ page, primary }) => {
  await atYou(page, await primary.newLink());
  await page.route(STEP, (route) =>
    route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"internal"}' }));
  const next = page.getByRole('button', { name: 'Continue' });
  await next.click();
  const alert = page.getByRole('alert');
  await expect(alert).toBeVisible();
  await alert.evaluate((el) => el.setAttribute('data-seen', ''));
  await next.click();
  await expect(page.locator('[role="alert"]:not([data-seen])')).toBeVisible(); // a new alert, which is read out
  await expect(alert).toHaveCount(1);

  await page.unroute(STEP);
  await next.click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Name your household');
  await expect(page.getByRole('alert')).toHaveCount(0);
});

test('the welcome screen says so too', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.route('**/api/v1/setup/steps/welcome', (route) => route.abort('internetdisconnected'));
  await page.getByRole('button', { name: 'Get started' }).click();
  await expect(page.getByRole('alert')).toContainText('This device couldn’t reach HomersLab');
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
});
