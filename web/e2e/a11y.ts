// Accessibility checks: axe-core's WCAG 2.2 AA rules on a surface's screens, as a person's device shows them. A test
// takes one look (a theme, and whether motion and transparency are reduced), opens its surface in it, and calls
// `accessible(page, 'name of the screen')` once per screen it reaches. Any violation fails the test, with a report that
// names the look, the screen, the rule and each element; the full axe results are attached to the HTML report.
import AxeBuilder from '@axe-core/playwright';
import { expect, type Page, test } from '@playwright/test';
import type { AxeResults, Result, RunOptions } from 'axe-core';

/** WCAG 2.0, 2.1 and 2.2, levels A and AA: axe-core's tags for them (2.2 adds only AA rules). */
export const WCAG22AA = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'];

export type Look = {
  theme: 'light' | 'dark';
  motion: 'no-preference' | 'reduce';
  transparency: 'no-preference' | 'reduce';
};

/** Every look a device can ask for: both themes, with and without reduced motion and reduced transparency. */
export const looks: Look[] = (['light', 'dark'] as const).flatMap((theme) =>
  (['no-preference', 'reduce'] as const).flatMap((motion) =>
    (['no-preference', 'reduce'] as const).map((transparency) => ({ theme, motion, transparency }))));

export const lookName = (l: Look): string =>
  [l.theme, l.motion === 'reduce' && 'reduced motion', l.transparency === 'reduce' && 'reduced transparency']
    .filter(Boolean).join(', ');

/**
 * Makes the page's device ask for this look, through the media features themselves (prefers-color-scheme,
 * prefers-reduced-motion, prefers-reduced-transparency), before anything is opened. Chromium's DevTools protocol is
 * the one way to set the third; the page's own matchMedia confirms all three took.
 */
export async function wear(page: Page, look: Look): Promise<void> {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setEmulatedMedia', {
    features: [
      { name: 'prefers-color-scheme', value: look.theme },
      { name: 'prefers-reduced-motion', value: look.motion },
      { name: 'prefers-reduced-transparency', value: look.transparency },
    ],
  });
  const seen = await page.evaluate(() => ({
    theme: matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light',
    motion: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'reduce' : 'no-preference',
    transparency: matchMedia('(prefers-reduced-transparency: reduce)').matches ? 'reduce' : 'no-preference',
  }));
  expect(seen, 'the browser emulates the look').toEqual(look);
}

/**
 * Runs axe's WCAG 2.2 AA rules on the page as it is now, once its screen has settled, and fails the test (softly, so
 * the test goes on to its next screens) on any violation.
 */
export async function accessible(page: Page, screen: string): Promise<void> {
  const results = await audit(page);
  await test.info().attach(`axe: ${screen}`, { body: JSON.stringify(results, null, 2), contentType: 'application/json' });
  expect.soft(report(results.violations), `axe (WCAG 2.2 AA) on “${screen}”`).toBe('');
}

/** axe's WCAG 2.2 AA results for the page as it is now, once its screen has settled. */
export async function audit(page: Page): Promise<AxeResults> {
  await settle(page);
  // The design system's glass draws its specular rim as an ::after over the whole card, masked to a 1px edge, and axe
  // gives up on every word above a large pseudo-element. The rim paints nothing under the words, so contrast is
  // measured as if pseudo-elements weren't there. Glass itself (a blur over the wallpaper) stays beyond axe: it measures
  // the card under reduced transparency, where every material is solid. (axe merges these check options into its
  // defaults; its types omit `checks`.) AxeBuilder's options() replaces what withTags() set, so the tags go in here.
  const options = {
    runOnly: { type: 'tag', values: WCAG22AA },
    checks: { 'color-contrast': { options: { ignorePseudo: true } } },
  } as RunOptions;
  return new AxeBuilder({ page }).options(options).analyze();
}

/**
 * Waits for the fonts, every finite animation (view transitions, entrances, the theme's glide), and then the layout:
 * a disclosure's ::details-content grows by a transition that getAnimations() doesn't list, so the boxes' layout (not
 * their transforms, which endless animations keep moving) must hold still for a few frames.
 */
async function settle(page: Page): Promise<void> {
  await page.evaluate(async () => {
    const frame = () => new Promise((r) => requestAnimationFrame(r));
    await document.fonts.ready;
    for (let i = 0; i < 5; i++) {
      const running = document.getAnimations().filter((a) => a.playState === 'running' && Number.isFinite(a.effect?.getComputedTiming().endTime ?? Infinity));
      if (!running.length) break;
      await Promise.allSettled(running.map((a) => a.finished));
    }
    const layout = () => {
      const boxes = [...document.body.querySelectorAll('*')].filter((e) => e instanceof HTMLElement);
      return boxes.map((e) => `${e.offsetLeft},${e.offsetTop},${e.offsetWidth},${e.offsetHeight}`).join(' ');
    };
    let last = layout();
    for (let still = 0, i = 0; still < 3 && i < 300; i++) {
      await frame();
      const now = layout();
      still = now === last ? still + 1 : 0;
      last = now;
    }
  });
}

/** One paragraph per violated rule: its impact, its help text and link, then each element with axe's reasons. */
export function report(violations: Result[]): string {
  return violations.map((v) => [
    `${v.id} (${v.impact ?? 'unknown impact'}): ${v.help}`,
    `  ${v.helpUrl}`,
    ...v.nodes.flatMap((n) => [
      `  - ${n.target.join(' ')}`,
      ...(n.failureSummary ?? '').split('\n').filter((s) => s.trim()).map((s) => `      ${s.trim()}`),
    ]),
  ].join('\n')).join('\n\n');
}
