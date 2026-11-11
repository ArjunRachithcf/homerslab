// First-time setup, opened from its one-time link: scripted from the setup prototype (prototypes/setup.html), with
// the real manager and agent behind the page.
import { expect, test } from './primary';

test('the link opens first-time setup, which shows the machine as its agent reports it', async ({ page, primary }) => {
  const link = await primary.newLink();
  await page.goto(link.localUrl);

  await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
  await expect(page.getByText('study-pc', { exact: true }).first()).toBeVisible();
  await expect(page).toHaveURL(/\/setup(#\/welcome)?$/); // the one-time key is gone from the address bar
  expect(await page.context().cookies()).toEqual([]); // the session rides in this tab, not in a cookie

  await page.getByText('About this machine').click();
  const facts = page.getByRole('list', { name: 'About this machine' });
  for (const fact of ['Fedora Linux 43 (Workstation Edition)', 'x86_64', '32 GB', 'Samsung SSD 980 PRO 1TB', '1 TB',
    'WDC WD40EFRX-68N', '4 TB', 'TPM 2.0', 'NVIDIA GeForce RTX 3060 Lite Hash Rate']) {
    await expect(facts).toContainText(fact);
  }
  const needs = page.getByRole('list', { name: 'You’ll need' });
  for (const need of ['Your mesh’s token', 'Your domain and its DNS provider’s token', 'The password of a mailbox',
    'A printer or a password manager', 'This laptop or your phone']) {
    await expect(needs).toContainText(need);
  }
  await expect(page.getByText('home server')).toHaveCount(0); // the glossary's words: a machine, not a server
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
  await expect(page.getByRole('radio', { name: /Authentik/ })).toBeChecked();
  await expect(page.getByRole('radio', { name: /Kanidm/ })).toBeDisabled(); // Kanidm's own ticket (#33)
  await expect(page.getByText('comes with a later version')).toBeVisible();
  await expect(next).toBeEnabled();

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

test('opening the same link again in the same browser carries on', async ({ page, primary }) => {
  const link = await primary.newLink();
  await page.goto(link.localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');
  await page.goto('about:blank');
  await page.goto(link.localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Welcome back');
});

test('a new link opened in the tab that shows setup is claimed there', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');
  await page.goto((await primary.newLink()).localUrl); // same page, only the fragment changes
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Welcome back');
  await page.getByRole('button', { name: 'Continue where I stopped' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');
});

// A full disk under the manager's database (#70): the page says so, instead of "can't reach HomersLab".
test('a full disk is said as such, and the answer typed stays', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  await page.getByLabel('Your name', { exact: true }).fill('Inês Ferreira');
  await page.getByLabel('Your mail address', { exact: true }).fill('ines@ferreira.family');
  await page.route('**/api/v1/setup/steps/you', (route) =>
    route.fulfill({ status: 507, contentType: 'application/json', body: '{"error":"disk-full"}' }));
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('alert')).toContainText('This machine’s disk is full');
  await expect(page.getByRole('heading', { level: 1 })).toContainText('What’s your name?');
  await expect(page.getByLabel('Your name', { exact: true })).toHaveValue('Inês Ferreira');

  await page.unroute('**/api/v1/setup/steps/you');
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Name your household');
});

test('a full disk when the page opens is said as such', async ({ page, primary }) => {
  await page.route('**/api/v1/setup', (route) =>
    route.fulfill({ status: 507, contentType: 'application/json', body: '{"error":"disk-full"}' }));
  await page.goto((await primary.newLink()).localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('This machine’s disk is full');
  await expect(page.getByText('Setup can’t reach HomersLab')).toHaveCount(0);
});

// Each refused answer says why, in plain words (#69).
test('a refused answer says why', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  const name = page.getByLabel('Your name', { exact: true });
  const mail = page.getByLabel('Your mail address', { exact: true });
  const next = page.getByRole('button', { name: 'Continue' });

  await name.fill('Inês\u202eevil');
  await mail.fill('ines@ferreira.family');
  await next.click();
  await expect(page.getByText('Your name has an invisible character')).toBeVisible();
  await expect(name).toHaveAttribute('aria-invalid', 'true');

  await name.fill('I'.repeat(65));
  await mail.fill(`${'a'.repeat(250)}@ferreira.family`);
  await next.click();
  await expect(page.getByText('Your name can be 64 characters at most.')).toBeVisible();
  await expect(page.getByText('A mail address can be 254 characters at most.')).toBeVisible();

  await name.fill('Inês Ferreira');
  await mail.fill('ines@ferreira.family');
  await next.click();
  const household = page.getByLabel('The household’s name', { exact: true });
  await household.fill('The Ferreira\u0007 household');
  await next.click();
  await expect(page.getByText('The name has an invisible character')).toBeVisible();
  await household.fill('H'.repeat(65));
  await next.click();
  await expect(page.getByText('The household’s name can be 64 characters at most.')).toBeVisible();
});

// The manager keeps a name in its normal form, and the page shows that form (#69).
test('a name is shown as the manager keeps it', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  await page.getByLabel('Your name', { exact: true }).fill('\uff29\uff4eê\uff53  Ferreira  ');
  await page.getByLabel('Your mail address', { exact: true }).fill('ines@ferreira.family');
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByLabel('The household’s name', { exact: true })).toHaveValue('The Ferreira household');
  await page.getByRole('button', { name: 'Back' }).click();
  await expect(page.getByLabel('Your name', { exact: true })).toHaveValue('Inês Ferreira');
});

// A long surname makes a household name the manager would refuse: the page cuts it short to fit, and never offers it.
test('the household name made from a long surname fits', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  const surname = 'Wolfeschlegelsteinhausenbergerdorffwelchevoralternwarengewissenhaft';
  await page.getByLabel('Your name', { exact: true }).fill(surname.slice(0, 58));
  await page.getByLabel('Your mail address', { exact: true }).fill('h@example.org');
  await page.getByRole('button', { name: 'Continue' }).click();
  const household = page.getByLabel('The household’s name', { exact: true });
  await expect(household).toHaveValue(`The ${surname.slice(0, 50)} household`);
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('How everyone signs in');
});

// A cut that ends inside a flag, a variant or a joined word leaves no tag, selector or tatweel hanging (#69).
test('the household name made from a surname with a flag fits, without a broken flag', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  const scotland = '\u{1F3F4}\u{E0067}\u{E0062}\u{E0073}\u{E0063}\u{E0074}\u{E007F}';
  await page.getByLabel('Your name', { exact: true }).fill(`Ana ${'X'.repeat(46)}${scotland}`);
  await page.getByLabel('Your mail address', { exact: true }).fill('ana@example.org');
  await page.getByRole('button', { name: 'Continue' }).click();
  const household = page.getByLabel('The household’s name', { exact: true });
  await expect(household).toHaveValue(`The ${'X'.repeat(46)} household`); // the flag whole, or not at all
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('How everyone signs in');
});

// The cut keeps an emoji ZWJ sequence whole, or leaves it out: never half a couple with a heart.
test('the household name made from a surname with a joined emoji fits, without a broken emoji', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  const couple = '\u{1F469}\u200D\u2764\uFE0F\u200D\u{1F468}';
  await page.getByLabel('Your name', { exact: true }).fill(`Ana ${'X'.repeat(46)}${couple}`);
  await page.getByLabel('Your mail address', { exact: true }).fill('ana@example.org');
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByLabel('The household’s name', { exact: true })).toHaveValue(`The ${'X'.repeat(46)} household`);
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('How everyone signs in');
});

// A cut that ends on a ZWNJ or ZWJ (Extend characters that stay with the letter before) leaves them hanging: the
// manager would refuse the name as hidden characters.
test('the household name made from a Persian surname cut at a ZWNJ is accepted', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await page.getByRole('button', { name: 'Get started' }).click();
  await page.getByLabel('Your name', { exact: true }).fill(`Ali ${'X'.repeat(48)}\u06CC\u200C\u062E\u0648\u0627\u0647\u0645`);
  await page.getByLabel('Your mail address', { exact: true }).fill('ali@example.org');
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByLabel('The household’s name', { exact: true })).toHaveValue(`The ${'X'.repeat(48)}\u06CC household`);
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('How everyone signs in');
});
