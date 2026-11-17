// Admin's screens under axe (WCAG 2.2 AA), in every look a device can ask for, at the projects' phone and desktop
// widths: the Store, an app's page, and an install plan, native and guarded.
import { accessible, looks, lookName, wear } from './a11y';
import { expect, test } from './primary';

for (const look of looks) {
  test(`Admin passes axe (WCAG 2.2 AA): ${lookName(look)}`, async ({ page, primary }) => {
    test.slow();
    await wear(page, look);
    const h1 = page.getByRole('heading', { level: 1 });

    primary.withDomain();
    await page.goto(await primary.adminURL());
    await expect(h1).toHaveText('Store');
    await page.getByRole('button', { name: 'Get Immich: see its install plan' }).click();
    await expect(page.getByRole('alert')).toContainText('Finish first-time setup first');
    await accessible(page, 'the Store, before setup');

    await primary.setUp();
    await page.reload();
    await expect(h1).toHaveText('Store');
    await accessible(page, 'the Store');

    await page.getByRole('link', { name: /Immich/ }).click();
    await expect(h1).toHaveText('Immich');
    await accessible(page, 'Immich’s page');

    await page.getByRole('button', { name: 'Get Immich: see its install plan' }).click();
    await expect(h1).toHaveText('Install Immich');
    await accessible(page, 'Immich’s install plan');

    await page.goto(await primary.adminURL());
    await page.getByRole('button', { name: 'Get BentoPDF: see its install plan' }).click();
    await expect(h1).toHaveText('Install BentoPDF');
    await accessible(page, 'BentoPDF’s install plan');

    await page.goto(new URL('/plans/p-0123', await primary.adminURL()).toString());
    await expect(h1).toHaveText('No such plan');
    await accessible(page, 'no such plan');
  });
}
