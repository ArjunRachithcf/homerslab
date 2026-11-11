// First-time setup's screens under axe (WCAG 2.2 AA), in every look a device can ask for, at the projects' phone and
// desktop widths: one walk through the screens per look, one line per screen.
import { accessible, audit, looks, lookName, report, WCAG22AA, wear } from './a11y';
import { expect, test } from './primary';

for (const look of looks) {
  test(`first-time setup passes axe (WCAG 2.2 AA): ${lookName(look)}`, async ({ page, primary }) => {
    test.slow(); // nine screens, each settled and audited: about 15 s alone, more beside other tests
    await wear(page, look);
    const h1 = page.getByRole('heading', { level: 1 });
    const next = page.getByRole('button', { name: 'Continue' });

    await page.goto((await primary.newLink()).localUrl.replace(/#.*/, ''));
    await expect(h1).toContainText('This link doesn’t work any more');
    await accessible(page, 'the link doesn’t work');

    await page.goto((await primary.newLink()).localUrl);
    await expect(h1).toContainText('Let’s set up HomersLab');
    await accessible(page, 'welcome');
    await page.getByText('About this machine').click();
    await accessible(page, 'welcome, about this machine open');

    await page.getByRole('button', { name: 'Get started' }).click();
    await expect(h1).toContainText('What’s your name?');
    await accessible(page, 'your name, empty');
    await page.getByLabel('Your name', { exact: true }).fill('Inês Ferreira');
    await page.getByLabel('Your mail address', { exact: true }).fill('<ines@ferreira.family>'); // the manager refuses it
    await next.click();
    await expect(page.getByText('Type a mail address, like name@example.org.')).toBeVisible();
    await accessible(page, 'your name, refused');
    await page.getByLabel('Your mail address', { exact: true }).fill('ines@ferreira.family');
    await next.click();

    await expect(h1).toContainText('Name your household');
    await accessible(page, 'your household');
    await next.click();

    await expect(h1).toContainText('How everyone signs in');
    await accessible(page, 'how everyone signs in (not built yet)');

    await primary.restartManager();
    await page.goto((await primary.newLink()).localUrl);
    await expect(h1).toContainText('Welcome back');
    await accessible(page, 'welcome back');

    await primary.stopManager();
    await page.evaluate(() => { location.hash = '#k=a-new-link'; }); // a new link arrives, and nothing answers
    await expect(h1).toContainText('Setup can’t reach HomersLab');
    await accessible(page, 'the manager can’t be reached');
  });
}

// The check itself: a sentence too faint to read and a field without a label, planted on the welcome screen.
test('the check catches too little contrast and a field without a label', async ({ page, primary }) => {
  await wear(page, { theme: 'light', motion: 'reduce', transparency: 'reduce' });
  await page.goto((await primary.newLink()).localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
  await page.locator('.as-lead').evaluate((lead) => lead.insertAdjacentHTML('afterend',
    '<p id="planted-faint" style="background: #fff; color: #ccc">A planted sentence</p><input id="planted-unlabelled" type="text">'));
  const { violations, passes, incomplete, inapplicable } = await audit(page);
  // the rules that ran are WCAG 2.0–2.2 A and AA, WCAG 2.2's own (target size, off in axe by default) among them
  const ran = [...violations, ...passes, ...incomplete, ...inapplicable];
  expect(ran.map((r) => r.id)).toContain('target-size');
  expect(ran.filter((r) => !r.tags.some((t) => WCAG22AA.includes(t))).map((r) => r.id)).toEqual([]);
  // with every material solid, axe measures the words on the card (the glass's rim doesn't hide them)
  const measured = passes.find((r) => r.id === 'color-contrast')?.nodes.map((n) => n.target.join(' '));
  expect(measured).toEqual(expect.arrayContaining(['#as-title', '.as-lead']));
  expect(violations.map((v) => v.id).sort()).toEqual(['color-contrast', 'label']);
  expect(report(violations)).toMatch(/color-contrast \(serious\)[^]*#planted-faint[^]*label \(critical\)[^]*#planted-unlabelled/);
});
