// The domain step's screen. Setup can't reach it in the real flow until the sign-in step is built (#17), so the
// manager's answers for setup's state are stood in here; the page, the link and its session are the real ones. What it
// checks: the page sends only the domain and where its DNS is, has no field for a token, and shows the terminal command
// (#64); then the certificate is issued and setup moves on.
import type { Page, Route } from '@playwright/test';
import { expect, test } from './primary';

type Step = { id: string; half: 'lan' | 'mesh'; state: string; secret?: string; command?: string };

function setupAt(domain: 'todo' | 'waiting' | 'done', answers: Record<string, string> = {}) {
  const ids = ['welcome', 'you', 'household', 'signin', 'network', 'domain', 'mail', 'kit', 'join', 'passkey', 'done'];
  const steps: Step[] = ids.map((id, i): Step => {
    const step: Step = { id, half: i < 8 ? 'lan' : 'mesh', state: i < 5 ? 'done' : 'todo' };
    if (id !== 'domain') return step;
    if (domain === 'waiting') return { ...step, state: 'waiting', secret: 'dns', command: 'sudo homerslab setup secret dns' };
    return { ...step, state: domain };
  });
  return {
    half: 'lan', current: domain === 'done' ? 'mail' : 'domain', steps,
    answers: { name: 'Inês Ferreira', mail: 'ines@ferreira.family', household: 'The Ferreira household', householdPort: '9443', ...answers },
  };
}

async function standIn(page: Page, state: () => unknown, onAnswer: (body: Record<string, unknown>) => unknown) {
  await page.route('**/api/v1/setup', (route: Route) => route.fulfill({ json: state() }));
  await page.route('**/api/v1/setup/steps/domain', async (route: Route) => {
    const body = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ json: onAnswer(body) });
  });
}

test('the domain comes from the mail address, the token goes in the terminal, and the certificate ends the wait', async ({ page, primary }) => {
  let domain: 'todo' | 'waiting' | 'done' = 'todo';
  const sent: Record<string, unknown>[] = [];
  await standIn(page, () => setupAt(domain, domain === 'todo' ? {} : { domain: 'ferreira.family', dnsProvider: 'infomaniak' }), (body) => {
    sent.push(body);
    domain = 'waiting';
    return setupAt(domain, { domain: 'ferreira.family', dnsProvider: 'infomaniak' });
  });
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Continue where I stopped' }).click();

  await expect(page.getByRole('heading', { level: 1 })).toContainText('Your domain');
  await expect(page.getByLabel('Domain', { exact: true })).toHaveValue('ferreira.family');
  await expect(page.getByRole('radio', { name: /Infomaniak/ })).toBeChecked();
  await expect(page.locator('input[type=password]')).toHaveCount(0);
  await page.getByRole('button', { name: 'Continue' }).click();

  await expect(page.getByRole('heading', { level: 1 })).toContainText('Now give HomersLab the DNS token, on this machine');
  await expect(page.getByText('sudo homerslab setup secret dns')).toBeVisible();
  await expect(page.getByRole('status')).toContainText('Waiting for the token and the certificate');
  await expect(page.getByRole('button', { name: 'Continue' })).toBeDisabled();
  await expect(page.locator('input')).toHaveCount(0);
  expect(sent).toEqual([{ domain: 'ferreira.family', provider: 'infomaniak' }]);

  domain = 'done'; // the admin ran the command, and the certificate was issued
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Sending mail', { timeout: 10_000 });
});

test('a DNS server of the household’s own needs its address and its key’s name, never its key', async ({ page, primary }) => {
  const sent: Record<string, unknown>[] = [];
  await standIn(page, () => setupAt('todo'), (body) => {
    sent.push(body);
    return setupAt('waiting', { domain: 'home.ferreira.family', dnsProvider: 'rfc2136' });
  });
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Continue where I stopped' }).click();
  await page.getByLabel('Domain', { exact: true }).fill('home.ferreira.family');
  await page.getByText('Your own DNS server').click();
  const next = page.getByRole('button', { name: 'Continue' });
  await expect(next).toBeDisabled();
  await page.getByLabel('The DNS server’s address').fill('203.0.113.53:53');
  await page.getByLabel('The TSIG key’s name').fill('homerslab');
  await expect(page.locator('input[type=password]')).toHaveCount(0);
  await next.click();
  await expect(page.getByText('sudo homerslab setup secret dns')).toBeVisible();
  expect(sent).toEqual([{ domain: 'home.ferreira.family', provider: 'rfc2136', server: '203.0.113.53:53', keyName: 'homerslab' }]);
});
