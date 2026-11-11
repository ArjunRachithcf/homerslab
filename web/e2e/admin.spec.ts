// Admin's Store and the install plan, scripted from the Admin prototype (prototypes/assets/admin-catalog.js), with the
// real manager and agent behind the page. The plan Admin shows is the one the CLI shows, by its id.
import { expect, test } from './primary';

test('Admin browses the catalog and opens an app’s install plan, the same plan the CLI shows', async ({ page, primary }) => {
  await primary.setUp();
  const admin = await primary.adminURL();
  expect(admin).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/store$/);
  await page.goto(admin);

  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Store');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark'); // Admin is dark by default
  await expect(page.getByText('The official catalog, from HomersLab')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Photos and videos' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Documents and files' })).toBeVisible();

  await page.getByRole('link', { name: /Immich/ }).click();
  await expect(page).toHaveURL(/\/store\/apps\/immich$/);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Immich');
  await expect(page.locator('.store-app-badges')).toHaveText('Verified');
  await expect(page.getByText('Native: with the household account, straight into the app')).toBeVisible();
  const shots = page.getByRole('group', { name: 'Screenshots, scroll sideways' }).getByRole('img');
  await expect(shots).toHaveCount(2);
  await expect(shots.first()).toHaveJSProperty('complete', true);
  expect(await shots.first().evaluate((img: HTMLImageElement) => img.naturalWidth)).toBeGreaterThan(0);

  await page.getByRole('button', { name: 'Get Immich: see its install plan' }).click();
  await expect(page).toHaveURL(/\/plans\/p-[0-9a-f]{32}$/);
  const id = new URL(page.url()).pathname.split('/').pop() ?? '';
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Install Immich');
  for (const words of ['photos.ferreira.family', 'Adults: Inês Ferreira', 'With their household account, straight into Immich.',
    'Creates a sign-in client, “Immich”, in Authentik.', 'The photo library', 'Every hour, on the hour.', 'study-pc',
    'In 4 containers: database, machine-learning, redis, server', 'None. This plan needs no passkey.', 'sealed to its TPM']) {
    await expect(page.getByText(words).first()).toBeVisible();
  }
  await expect(page.getByText(id).first()).toBeVisible();
  await expect(page.getByRole('button', { name: 'Install' })).toBeDisabled();

  // The same plan, by its id, in the CLI.
  const cli = await primary.cli('plan', 'show', id);
  expect(cli).toContain(`Plan ${id}`);
  expect(cli).toContain('photos.ferreira.family');
  expect(JSON.parse(await primary.cli('plan', 'immich', '--json')).id).toBe(id);

  // A plan's address works on its own, and the page reloads to the same plan.
  await page.reload();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Install Immich');
});

test('a guarded app’s plan says the edge signs people in', async ({ page, primary }) => {
  await primary.setUp();
  await page.goto(await primary.adminURL());
  await page.getByRole('button', { name: 'Get BentoPDF: see its install plan' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Install BentoPDF');
  await expect(page.getByText('The edge asks for the household account first.')).toBeVisible();
  await expect(page.getByText('Nothing to back up: it keeps no data.')).toBeVisible();
  await expect(page.getByText('pdf.ferreira.family').first()).toBeVisible();
});

test('before first-time setup, Get says what’s missing', async ({ page, primary }) => {
  await page.goto(await primary.adminURL());
  await page.getByRole('button', { name: 'Get Immich: see its install plan' }).click();
  await expect(page.getByRole('alert')).toContainText('Finish first-time setup first');
  await expect(page).toHaveURL(/\/store$/);
});
