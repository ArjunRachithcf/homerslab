// Planted problems, each on a screen of first-time setup that otherwise passes: each test here must fail, through
// accessible() itself. They run only inside the check of the check (a11y-check.spec.ts), which runs them in a
// Playwright of their own and reads how they failed; anywhere else they are skipped.
import { accessible, wear, type Look } from './a11y';
import { expect, test } from './primary';

test.skip(!process.env.HOMERSLAB_PLANTED, 'run by a11y-check.spec.ts');

// css: [selector, property, value], set through the CSSOM (the page's CSP refuses inline style sheets)
// cut: the viewport made short enough that its bottom edge runs through the middle of the lead. (A lead whose top is
// cut by a few pixels, or its last 10px, is still measured; through its middle, axe can't decide it.)
// hideOnScroll: the first scroll (accessible() bringing the cut lead into view) hides the lead, so axe's second run
// can't see it at all
// after: where html goes (after the lead, unless it says)
const plants: { name: string; look: Look; css?: [string, string, string][]; html?: string; after?: string; cut?: boolean; hideOnScroll?: boolean }[] = [
  { name: 'a field without a label, light', look: { theme: 'light', motion: 'reduce', transparency: 'no-preference' },
    html: '<input id="planted-unlabelled" type="text">' },
  { name: 'a field without a label, dark', look: { theme: 'dark', motion: 'reduce', transparency: 'no-preference' },
    html: '<input id="planted-unlabelled" type="text">' },
  { name: 'a faint lead on a gradient that looks solid', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    css: [['.assistant', 'background-image', 'linear-gradient(#fff, #fefefe)'], ['.as-lead', 'color', '#c8c8c8']] },
  { name: 'a faint lead the viewport’s edge cuts', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    css: [['.as-lead', 'color', '#c8c8c8']], cut: true },
  { name: 'a cut lead the second run can’t see', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    cut: true, hideOnScroll: true },
  // axe can't measure words over glass (it doesn't decide them, and that passes while materials are translucent)
  { name: 'a faint lead on glass', look: { theme: 'light', motion: 'reduce', transparency: 'no-preference' },
    css: [['.as-lead', 'color', '#a3a3a3']] },
  // nor words on the wallpaper, which it can't see: it measures them against the page's ground colour, even with every
  // material solid. This grey keeps 4.5:1 on the ground (#f5f5f7) and loses it on the wallpaper.
  { name: 'a faint footer on the wallpaper', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    css: [['.as-foot', 'color', '#6b6b6b']] },
  // the lead's own ink, faded: its opacity goes into the ink's alpha
  { name: 'a faded lead on glass', look: { theme: 'dark', motion: 'reduce', transparency: 'no-preference' },
    css: [['.as-lead', 'opacity', '0.35']] },
  // a word the viewport can't show whole, wherever the page scrolls, isn't measured: that fails too (outside the card:
  // the card's backdrop-filter would hold a fixed box, and clip it)
  { name: 'a word half off the screen', look: { theme: 'light', motion: 'reduce', transparency: 'no-preference' },
    html: '<p id="planted-offscreen">Half off the screen</p>', after: '.as-foot',
    css: [['#planted-offscreen', 'position', 'fixed'], ['#planted-offscreen', 'left', '-40px'], ['#planted-offscreen', 'top', '40%']] },
];

for (const plant of plants) {
  test(`planted: ${plant.name}`, async ({ page, primary }) => {
    await wear(page, plant.look);
    await page.goto((await primary.newLink()).localUrl);
    await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
    const html = plant.html;
    if (html) await page.locator(plant.after ?? '.as-lead').evaluate((el, h) => el.insertAdjacentHTML('afterend', h), html);
    for (const [sel, prop, value] of plant.css ?? []) {
      await page.locator(sel).evaluate((el, pv) => (el as HTMLElement).style.setProperty(pv.prop, pv.value, 'important'), { prop, value });
    }
    if (plant.cut) {
      const r = await page.locator('.as-lead').evaluate((lead) => lead.getBoundingClientRect().toJSON() as DOMRect);
      await page.setViewportSize({ width: page.viewportSize()?.width ?? 1280, height: Math.round(r.top + r.height / 2) });
      expect(await page.locator('.as-lead').evaluate((lead) => {
        const b = lead.getBoundingClientRect();
        return b.top >= 0 && b.top < innerHeight && b.bottom > innerHeight;
      }), 'the viewport’s edge cuts the lead').toBe(true);
    }
    if (plant.hideOnScroll) {
      await page.locator('.as-lead').evaluate((lead) => document.addEventListener('scroll',
        () => (lead as HTMLElement).style.setProperty('visibility', 'hidden', 'important'), { once: true, capture: true }));
    }
    await accessible(page, `planted: ${plant.name}`);
  });
}
