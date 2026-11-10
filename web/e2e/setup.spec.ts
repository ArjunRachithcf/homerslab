// First-time setup, opened from its one-time link: scripted from the setup prototype (prototypes/setup.html), with
// the real manager and agent behind the page.
import { expect, test } from './primary';

test('the link opens first-time setup, which shows the machine as its agent reports it', async ({ page, primary }) => {
  const link = await primary.newLink();
  await page.goto(link.localUrl);

  await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
  await expect(page.getByText('study-pc', { exact: true }).first()).toBeVisible();
  await expect(page).toHaveURL(/\/setup(#\/welcome)?$/); // the one-time key is gone from the address bar

  await page.getByText('About this machine').click();
  const facts = page.getByRole('list', { name: 'About this machine' });
  for (const fact of ['Fedora Linux 43 (Workstation Edition)', 'x86_64', '32 GB', 'Samsung SSD 980 PRO 1TB', '1 TB',
    'WDC WD40EFRX-68N', '4 TB', 'TPM 2.0', 'NVIDIA GeForce RTX 3060 Lite Hash Rate']) {
    await expect(facts).toContainText(fact);
  }
});

test('setup takes one question per screen, and resumes where it stopped', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();

  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');
  const next = page.getByRole('button', { name: 'Continue' });
  await expect(next).toBeDisabled();
  await page.getByLabel('Your name', { exact: true }).fill('Inês Ferreira');
  await page.getByLabel('Your mail address', { exact: true }).fill('ines@ferreira.family');
  await next.click();

  await expect(page.getByRole('heading', { level: 1 })).toContainText('Name your household');
  await expect(page.getByLabel('The household’s name', { exact: true })).toHaveValue('The Ferreira household');
  await next.click();

  await expect(page.getByRole('heading', { level: 1 })).toContainText('How everyone signs in');
  await expect(page.getByText('comes with a later version')).toBeVisible();
  await expect(next).toBeDisabled();

  // Interrupted: the browser is closed, the machine restarts, and a new link is made.
  await primary.restartManager();
  await page.goto((await primary.newLink()).localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Welcome back');
  await expect(page.getByText('You stopped at How everyone signs in.')).toBeVisible();
  const left = page.getByRole('list', { name: 'Still to do' });
  for (const step of ['How everyone signs in', 'Your private network', 'Your domain', 'Sending mail', 'Your recovery kit', 'Put this device on the mesh']) {
    await expect(left).toContainText(step);
  }
  await expect(left).not.toContainText('What’s your name?');

  await page.getByRole('button', { name: 'Continue where I stopped' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('How everyone signs in');
  await page.getByRole('button', { name: 'Back' }).click();
  await page.getByRole('button', { name: 'Back' }).click();
  await expect(page.getByLabel('Your mail address', { exact: true })).toHaveValue('ines@ferreira.family');
});

test('a link works once', async ({ browser, primary }) => {
  const link = await primary.newLink();
  const first = await browser.newPage();
  await first.goto(link.localUrl);
  await expect(first.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');

  const second = await (await browser.newContext()).newPage();
  await second.goto(link.localUrl);
  await expect(second.getByRole('heading', { level: 1 })).toContainText('This link doesn’t work any more');
  await expect(second.getByText('sudo homerslab setup link')).toBeVisible();
});

test('the page without a link says how to get one', async ({ page, primary }) => {
  const link = await primary.newLink();
  await page.goto(link.localUrl.replace(/#.*/, ''));
  await expect(page.getByRole('heading', { level: 1 })).toContainText('This link doesn’t work any more');
});
