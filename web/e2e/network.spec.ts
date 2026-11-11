// The network step's screen. Setup can't reach it in the real flow until the sign-in step is built (#17), so the
// manager's answers for setup's state are stood in here; the page, the link and its session are the real ones. What it
// checks: the page sends only non-secret answers, has no field for a token, and shows the terminal command (#64).
import type { Page, Route } from '@playwright/test';
import { expect, test } from './primary';

type Step = { id: string; half: 'lan' | 'mesh'; state: string; secret?: string; command?: string };

function setupAt(network: 'todo' | 'waiting' | 'done', answers: Record<string, string> = {}) {
  const ids = ['welcome', 'you', 'household', 'signin', 'network', 'domain', 'mail', 'kit', 'join', 'passkey', 'done'];
  const steps: Step[] = ids.map((id, i) => ({ id, half: i < 8 ? 'lan' : 'mesh', state: i < 4 ? 'done' : 'todo' }));
  steps[4] = network === 'waiting'
    ? { ...steps[4], state: 'waiting', secret: 'mesh', command: 'sudo homerslab setup secret mesh' }
    : { ...steps[4], state: network };
  const current = network === 'done' ? 'domain' : 'network';
  return { half: 'lan', current, steps, answers: { name: 'Inês Ferreira', mail: 'ines@ferreira.family', household: 'The Ferreira household', ...answers } };
}

async function standIn(page: Page, state: () => unknown, onAnswer: (body: Record<string, unknown>) => unknown) {
  await page.route('**/api/v1/setup', (route: Route) => route.fulfill({ json: state() }));
  await page.route('**/api/v1/setup/steps/network', async (route: Route) => {
    const body = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ json: onAnswer(body) });
  });
}

test('on NetBird’s cloud the page asks no token, and shows the command for the terminal', async ({ page, primary }) => {
  let network: 'todo' | 'waiting' | 'done' = 'todo';
  const sent: Record<string, unknown>[] = [];
  await standIn(page, () => setupAt(network), (body) => {
    sent.push(body);
    network = 'waiting';
    return setupAt(network, { mesh: 'netbird', meshControlPlane: 'cloud' });
  });
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Continue where I stopped' }).click();

  await expect(page.getByRole('heading', { level: 1 })).toContainText('Your private network');
  await expect(page.getByRole('radio', { name: /NetBird’s cloud/ })).toBeChecked();
  await expect(page.locator('input[type=password]')).toHaveCount(0);
  await page.getByRole('button', { name: 'Continue' }).click();

  await expect(page.getByRole('heading', { level: 1 })).toContainText('Now give HomersLab NetBird’s token, on this machine');
  await expect(page.getByText('sudo homerslab setup secret mesh')).toBeVisible();
  await expect(page.getByRole('status')).toContainText('Waiting for the token');
  await expect(page.getByRole('button', { name: 'Continue' })).toBeDisabled();
  await expect(page.locator('input')).toHaveCount(0);
  expect(sent).toEqual([{ mesh: 'netbird', controlPlane: 'cloud' }]);

  network = 'done'; // the admin ran the command
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Your domain', { timeout: 10_000 });
});

test('a self-hosted NetBird needs its https address, and only that', async ({ page, primary }) => {
  const sent: Record<string, unknown>[] = [];
  let network: 'todo' | 'done' = 'todo';
  await standIn(page, () => setupAt(network), (body) => {
    sent.push(body);
    network = 'done';
    return setupAt(network, { mesh: 'netbird', meshControlPlane: 'self-hosted', meshUrl: 'https://netbird.ferreira.family' });
  });
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Continue where I stopped' }).click();
  await page.getByText('Your own NetBird server').click();
  const next = page.getByRole('button', { name: 'Continue' });
  const address = page.getByLabel('Its address', { exact: true });
  await address.fill('http://netbird.ferreira.family');
  await expect(next).toBeDisabled();
  await address.fill('https://netbird.ferreira.family');
  await next.click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Your domain');
  expect(sent).toEqual([{ mesh: 'netbird', controlPlane: 'self-hosted', url: 'https://netbird.ferreira.family' }]);
});
