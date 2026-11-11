// Reduced motion: what the design system styles opens and closes at once, "More options" included.
import type { Page } from '@playwright/test';
import path from 'node:path';
import { expect, test } from './primary';

const gallery = `file://${path.join(path.dirname(new URL(import.meta.url).pathname), '..', '..', 'design', 'index.html')}`;

/** Opens the page's "More options" and says how it moved: its transitions, and its height then and a moment later. */
async function openMore(page: Page) {
  const more = page.locator('details.more').first();
  await more.locator('summary').click();
  await expect(more).toHaveAttribute('open', '');
  const opened = await more.evaluate((el) => ({
    durations: getComputedStyle(el, '::details-content').transitionDuration,
    height: el.getBoundingClientRect().height,
    animations: el.getAnimations({ subtree: true }).filter((a) => a.playState === 'running').length,
  }));
  await page.waitForTimeout(700); // longer than the smooth spring, 558 ms
  const settled = await more.evaluate((el) => el.getBoundingClientRect().height);
  return { ...opened, settled };
}

const still = (durations: string) => durations.split(',').every((d) => parseFloat(d) === 0);

test.describe('under prefers-reduced-motion', () => {
  test.use({ reducedMotion: 'reduce' });

  test('"More options" opens at once', async ({ page, primary }) => {
    await page.goto((await primary.newLink()).localUrl);
    await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
    const m = await openMore(page);
    expect(still(m.durations), `transition-duration of ::details-content: ${m.durations}`).toBe(true);
    expect(m.animations).toBe(0);
    expect(m.height).toBe(m.settled);
  });

  test('the design system\'s gallery opens "More options" at once', async ({ page }) => {
    await page.goto(gallery);
    await page.locator('details.more summary').scrollIntoViewIfNeeded();
    const m = await openMore(page);
    expect(still(m.durations), `transition-duration of ::details-content: ${m.durations}`).toBe(true);
    expect(m.animations).toBe(0);
    expect(m.height).toBe(m.settled);
  });
});

test('"More options" opens at once with the design system\'s own reduced motion', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
  await page.evaluate(() => document.documentElement.setAttribute('data-motion', 'reduce'));
  const m = await openMore(page);
  expect(still(m.durations), `transition-duration of ::details-content: ${m.durations}`).toBe(true);
  expect(m.animations).toBe(0);
  expect(m.height).toBe(m.settled);
});

test('"More options" still glides open with motion on', async ({ page, primary }) => {
  await page.goto((await primary.newLink()).localUrl);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Let’s set up HomersLab');
  const m = await openMore(page);
  expect(still(m.durations)).toBe(false); // the check above can tell the two apart
  expect(m.height).toBeLessThan(m.settled);
});
