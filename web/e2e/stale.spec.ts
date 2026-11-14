// A duplicated tab holds the same session (sessionStorage is copied), so it could answer a step another tab already
// answered and overwrite it (#110). The manager refuses the older view's answer as stale; the page says so.
import { expect, test } from './primary';

test('a duplicated tab’s answer to a step the other tab answered is refused, and what the other kept stays', async ({ context, page, primary }) => {
  const link = await primary.newLink();
  await page.goto(link.localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');

  // Duplicate the tab: same session token, same view of the step.
  const token = await page.evaluate(() => sessionStorage.getItem('homerslab.setup.session'));
  expect(token).toBeTruthy();
  const copy = await context.newPage();
  await copy.addInitScript((t) => sessionStorage.setItem('homerslab.setup.session', t as string), token);
  await copy.goto(new URL('/setup', link.localUrl).href);
  await copy.getByRole('button', { name: 'Continue where I stopped' }).click();
  await expect(copy.getByRole('heading', { level: 1 })).toContainText('What’s your name?');

  // The first tab answers.
  await page.getByLabel('Your name', { exact: true }).fill('Inês Ferreira');
  await page.getByLabel('Your mail address', { exact: true }).fill('ines@ferreira.family');
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Name your household');

  // The copy still shows the question open, and its answer is refused.
  await copy.getByLabel('Your name', { exact: true }).fill('Someone Else');
  await copy.getByLabel('Your mail address', { exact: true }).fill('else@example.org');
  await copy.getByRole('button', { name: 'Continue' }).click();
  await expect(copy.getByRole('alert')).toContainText('changed in another tab or browser');
  await expect(copy.getByLabel('Your name', { exact: true })).toHaveValue('Someone Else'); // what was typed stays until reload

  await copy.getByRole('alert').getByRole('button', { name: 'Reload' }).click();
  await expect(copy.getByLabel('Your name', { exact: true })).toHaveValue('Inês Ferreira');
  await expect(copy.getByLabel('Your mail address', { exact: true })).toHaveValue('ines@ferreira.family');
});

test('changing a done step again, as an edit from a page that is current, is kept', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  await page.getByLabel('Your name', { exact: true }).fill('Inês Ferreira');
  await page.getByLabel('Your mail address', { exact: true }).fill('ines@ferreira.family');
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Name your household');
  await page.getByRole('button', { name: 'Back' }).click();
  await page.getByLabel('Your name', { exact: true }).fill('Inês F. Ferreira');
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Name your household');
  await page.getByRole('button', { name: 'Back' }).click();
  await expect(page.getByLabel('Your name', { exact: true })).toHaveValue('Inês F. Ferreira');
});
