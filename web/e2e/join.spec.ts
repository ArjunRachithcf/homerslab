// First-time setup's second half on the LAN page: it shows the terminal command that hands over the device key and
// the passkey's link, never the credentials themselves (#64), and once the admin has signed in over the mesh it says
// setup continues in Admin. The manager's answers for setup's state are stood in; the page, the link and its session
// are the real ones.
import type { Page, Route } from '@playwright/test';
import { expect, test } from './primary';

const ids = ['welcome', 'you', 'household', 'signin', 'network', 'domain', 'mail', 'kit', 'join', 'passkey', 'done'];
const state = {
  half: 'mesh', current: 'join',
  steps: ids.map((id, i) => ({ id, half: i < 8 ? 'lan' : 'mesh', state: i < 8 ? 'done' : 'todo' })),
  answers: { name: 'Inês Ferreira', household: 'The Ferreira household', domain: 'example.test', adminUrl: 'https://admin.example.test:9443' },
};

async function standIn(page: Page, closed: () => boolean) {
  await page.route('**/api/v1/setup', (route: Route) =>
    closed() ? route.fulfill({ status: 410, json: { error: 'lan-closed' } }) : route.fulfill({ json: state }));
}

test('the second half shows the terminal command, and Admin once the admin has signed in over the mesh', async ({ page, primary }) => {
  let closed = false;
  await standIn(page, () => closed);
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Continue where I stopped' }).click();

  await expect(page.getByRole('heading', { level: 1 })).toContainText('Put this device on the mesh');
  await expect(page.getByText('sudo homerslab setup join')).toBeVisible();
  await expect(page.getByRole('status')).toContainText('Waiting for your first sign-in');
  await expect(page.locator('input')).toHaveCount(0);
  await expect(page.locator('img, canvas, svg[aria-label*="QR" i]')).toHaveCount(0); // no code on the LAN page

  closed = true; // the admin signed in to Admin over the mesh: this listener answers nothing any more
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Setup continues in Admin', { timeout: 10_000 });
  await expect(page.getByRole('link', { name: 'Open Admin' })).toHaveAttribute('href', 'https://admin.example.test:9443');
});
