// Home's screens under axe (WCAG 2.2 AA) and the pixel contrast check, in every look a device can ask for, at the
// projects' phone and desktop widths (#21): an adult's panels with an app not working, a kid's tiles, an admin's Home,
// the person's menu, an empty Home, and signed out.
import { accessible, audit, lookName, looks, report, wear } from './a11y';
import { expect, ines, test, tomas } from './home';

for (const look of looks) {
  test(`Home passes axe (WCAG 2.2 AA) and the contrast check: ${lookName(look)}`, async ({ page, home }) => {
    test.slow();
    await wear(page, look);
    const h1 = page.getByRole('heading', { level: 1 });

    await page.goto(home.url('home'));
    await expect(h1).toContainText('Donetick isn’t working.');
    await accessible(page, 'an adult’s Home, an app not working');

    // The menu covers the page's words, which the pixel check can't measure under it: axe alone, there.
    await page.getByRole('button', { name: 'Rui Ferreira' }).click();
    await expect(page.getByRole('dialog', { name: 'Your account' })).toBeVisible();
    expect.soft(report((await audit(page)).violations), 'axe (WCAG 2.2 AA) on the person’s menu').toBe('');
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog', { name: 'Your account' })).toBeHidden();

    await page.getByRole('button', { name: 'Rui Ferreira' }).click();
    await page.getByRole('button', { name: /Sign out/ }).click();
    await expect(h1).toHaveText('You’re signed out');
    await accessible(page, 'signed out');

    home.person = tomas;
    await page.goto(home.url('home'));
    await expect(h1).toContainText('Hi, Tomás.');
    await accessible(page, 'a kid’s Home');

    await page.context().clearCookies();
    home.person = ines;
    await page.goto(home.url('home'));
    await expect(page.getByRole('heading', { name: 'For you as admin' })).toBeVisible();
    await accessible(page, 'an admin’s Home');

    await home.write([]);
    await page.reload();
    await expect(h1).toContainText('Nothing here yet.');
    await accessible(page, 'an empty Home');
  });
}
