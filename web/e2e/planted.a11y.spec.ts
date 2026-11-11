// Planted problems, each on a screen of first-time setup that otherwise passes: each test here must fail, through
// accessible() itself. They run only inside the check of the check (a11y-check.spec.ts), which runs them in a
// Playwright of their own and reads how they failed; anywhere else they are skipped.
import { accessible, wear, type Look } from './a11y';
import { expect, test } from './primary';

test.skip(!process.env.HOMERSLAB_PLANTED, 'run by a11y-check.spec.ts');

// css: [selector, property, value], set through the CSSOM (the page's CSP refuses inline style sheets)
const plants: { name: string; look: Look; css?: [string, string, string][]; html?: string }[] = [
  { name: 'a field without a label, light', look: { theme: 'light', motion: 'reduce', transparency: 'no-preference' },
    html: '<input id="planted-unlabelled" type="text">' },
  { name: 'a field without a label, dark', look: { theme: 'dark', motion: 'reduce', transparency: 'no-preference' },
    html: '<input id="planted-unlabelled" type="text">' },
  { name: 'a faint lead on a gradient that looks solid', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    css: [['.assistant', 'background-image', 'linear-gradient(#fff, #fefefe)'], ['.as-lead', 'color', '#c8c8c8']] },
];

for (const plant of plants) {
  test(`planted: ${plant.name}`, async ({ page, primary }) => {
    await wear(page, plant.look);
    await page.goto((await primary.newLink()).localUrl);
    await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
    for (const [sel, prop, value] of plant.css ?? []) {
      await page.locator(sel).evaluate((el, pv) => (el as HTMLElement).style.setProperty(pv.prop, pv.value, 'important'), { prop, value });
    }
    const html = plant.html;
    if (html) await page.locator('.as-lead').evaluate((lead, h) => lead.insertAdjacentHTML('afterend', h), html);
    await accessible(page, `planted: ${plant.name}`);
  });
}
