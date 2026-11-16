// Admin's Store and the install plan, scripted from the Admin prototype (prototypes/assets/admin-catalog.js), with the
// real manager and agent behind the page. The plan Admin shows is the one the CLI shows, by its id.
import { expect, test } from './primary';

test('Admin browses the catalog and opens an app’s install plan, the same plan the CLI shows', async ({ page, primary }) => {
  await primary.setUp();
  await page.goto(await primary.adminURL());

  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Store');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark'); // Admin is dark by default
  await expect(page.getByText('The official catalog, from Homeward')).toBeVisible();
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
  await expect(page.getByText('BentoPDF learns who someone is from the edge alone', { exact: false })).toBeVisible();
  await expect(page.getByText('Nothing to back up: it keeps no data.')).toBeVisible();
  await expect(page.getByText('pdf.ferreira.family').first()).toBeVisible();
});

test('a guarded app’s plan installs from its page; before the household account is set up, Install says so', async ({ page, primary }) => {
  await primary.setUp();
  await page.goto(await primary.adminURL());
  await page.getByRole('button', { name: 'Get BentoPDF: see its install plan' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Install BentoPDF');
  const install = page.getByRole('button', { name: 'Install', exact: true });
  await expect(install).toBeEnabled();
  await install.click();
  await expect(page.getByRole('alert')).toContainText('the household account isn’t set up yet');
  await expect(install).toBeEnabled(); // nothing changed: the same plan can be applied once the account is there
});

test('an app whose own sign-in Homeward can’t set up yet can’t be installed, and says why', async ({ page, primary }) => {
  await primary.setUp();
  await page.goto(await primary.adminURL());
  await page.getByRole('button', { name: 'Get Immich: see its install plan' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Install Immich');
  await expect(page.getByRole('button', { name: 'Install', exact: true })).toBeDisabled();
  await expect(page.getByText('Homeward can’t set up Immich’s own sign-in yet: that comes with a later version.')).toBeVisible();
});

test('an app that signs people in itself: its plan names its client and where it sends people back, and it installs from its page (#20)', async ({ page, primary }) => {
  await primary.setUp();
  await page.goto(await primary.adminURL());
  await page.getByRole('button', { name: 'Get Mealie: see its install plan' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Install Mealie');
  for (const words of ['With their household account, straight into Mealie.', 'Creates a sign-in client, “Mealie”, in Authentik.',
    /It sends people back to Mealie only at https:\/\/recipes\.ferreira\.family(:\d+)?\/login\./, 'A sign-in client, “Mealie”, in Authentik']) {
    await expect(page.getByText(words).first()).toBeVisible();
  }
  const install = page.getByRole('button', { name: 'Install', exact: true });
  await expect(install).toBeEnabled();
  await install.click();
  await expect(page.getByRole('alert')).toContainText('the household account isn’t set up yet');
});

test('before first-time setup, Get says what’s missing', async ({ page, primary }) => {
  primary.withDomain();
  await page.goto(await primary.adminURL());
  const store = primary.url('admin', '/'); // where the sign-in leaves the admin: Admin's start, the Store
  await expect(page).toHaveURL(store);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Store');
  await page.getByRole('button', { name: 'Get Immich: see its install plan' }).click();
  await expect(page.getByRole('alert')).toContainText('Finish first-time setup first');
  await expect(page).toHaveURL(store);
});

test('the admin’s cookie comes from the sign-in, and Admin’s calls carry it', async ({ page, primary }) => {
  await primary.setUp();
  await page.goto(await primary.adminURL());
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Store');
  const [cookie] = (await page.context().cookies()).filter((c) => c.name === '__Host-homerslab-session');
  expect(cookie).toMatchObject({ domain: 'admin.ferreira.family', path: '/', secure: true, httpOnly: true, sameSite: 'Lax' });
  const catalog = primary.seen.filter((r) => r.path === '/api/v1/catalog');
  expect(catalog.length).toBeGreaterThan(0);
  expect(catalog.every((r) => r.session && r.status === 200)).toBe(true);
});

test('another of the household’s names can’t make a change in Admin, though the browser sends the admin’s cookie with it', async ({ page, primary }) => {
  await primary.setUp();
  const admin = await primary.adminURL();
  await page.goto(admin);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Store');
  const plans = new URL('/api/v1/plans', admin).href;
  const body = JSON.stringify({ app: 'bentopdf' });

  // One of the household's apps is the same site as Admin, so SameSite=Lax lets the cookie go: Admin's own checks
  // (the page's Origin, Sec-Fetch-Site) must refuse the change. A fetch that needs no preflight, then a form.
  await page.goto(primary.url('pdf'));
  await page.evaluate(async ({ to, b }) => {
    await fetch(to, { method: 'POST', mode: 'no-cors', credentials: 'include', headers: { 'Content-Type': 'text/plain' }, body: b });
  }, { to: plans, b: body });
  const answer = page.waitForResponse(plans);
  await page.evaluate(({ to, b }) => {
    const form = Object.assign(document.createElement('form'), { action: to, method: 'post', enctype: 'text/plain' });
    // text/plain sends name=value: the body is {"app":"bentopdf","x":"="}, JSON that a form can forge
    form.append(Object.assign(document.createElement('input'), { name: `${b.slice(0, -1)},"x":"`, value: '"}' }));
    document.body.append(form);
    form.submit();
  }, { to: plans, b: body });
  expect((await answer).status()).toBe(403);

  const posts = primary.seen.filter((r) => r.method === 'POST' && r.path === '/api/v1/plans');
  expect(posts).toEqual([
    { host: 'admin.ferreira.family', method: 'POST', path: '/api/v1/plans', session: true, status: 403 },
    { host: 'admin.ferreira.family', method: 'POST', path: '/api/v1/plans', session: true, status: 403 },
  ]);
  expect(primary.plans()).toBe(0);
});
