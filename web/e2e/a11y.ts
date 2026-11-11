// Accessibility checks: axe-core's WCAG 2.2 AA rules on a surface's screens, as a person's device shows them. A test
// takes one look (a theme, and whether motion and transparency are reduced), opens its surface in it, and calls
// `accessible(page, 'name of the screen')` once per screen it reaches. Any violation fails the test (and, with every
// material solid, anything axe couldn't decide), with a report that names the look, the screen, the rule and each
// element; axe's findings are attached to the HTML report.
import AxeBuilder from '@axe-core/playwright';
import { expect, type Page, test } from '@playwright/test';

// axe-core's types, as the AxeBuilder in use has them (axe-core itself is its dependency, not ours)
type AxeResults = Awaited<ReturnType<AxeBuilder['analyze']>>;
type Result = AxeResults['violations'][number];
type RunOptions = Parameters<AxeBuilder['options']>[0];

/** WCAG 2.0, 2.1 and 2.2, levels A and AA: axe-core's tags for them (2.2 adds only AA rules). */
export const WCAG22AA = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'];

/**
 * axe's experimental rules for WCAG A and AA, which its tags leave out: by name. css-orientation-lock (1.3.4, AA)
 * matters on phones. The rest of its experimental rules are best practices.
 */
export const EXPERIMENTAL_WCAG = ['css-orientation-lock', 'p-as-heading', 'table-fake-caption', 'td-has-header'];

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

const worn = new WeakMap<Page, Look>();

/**
 * Makes the page's device ask for this look, through the media features themselves (prefers-color-scheme,
 * prefers-reduced-motion, prefers-reduced-transparency), before anything is opened. Chromium's DevTools protocol is
 * the one way to set the third, so other browsers skip the test; the session stays open, as the look lasts only as
 * long as it does. The page's own matchMedia confirms the look here and again at every check.
 */
export async function wear(page: Page, look: Look): Promise<void> {
  test.skip(page.context().browser()?.browserType().name() !== 'chromium', 'the looks are set through Chromium’s DevTools protocol');
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setEmulatedMedia', {
    features: [
      { name: 'prefers-color-scheme', value: look.theme },
      { name: 'prefers-reduced-motion', value: look.motion },
      { name: 'prefers-reduced-transparency', value: look.transparency },
    ],
  });
  worn.set(page, look);
  expect(await seen(page), 'the browser shows the look').toEqual(look);
}

const seen = (page: Page): Promise<Look> => page.evaluate(() => ({
  theme: matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light',
  motion: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'reduce' : 'no-preference',
  transparency: matchMedia('(prefers-reduced-transparency: reduce)').matches ? 'reduce' : 'no-preference',
}));

/**
 * Runs axe's WCAG 2.2 AA rules on the page as it is now, once its screen has settled, and fails the test (softly, so
 * the test goes on to its next screens) on any violation. Under reduced transparency every material is solid, so
 * whatever axe couldn't decide fails too, once each undecided word the viewport's edge cuts has been measured again in
 * view. What axe found or couldn't decide is attached to the report.
 */
export async function accessible(page: Page, screen: string): Promise<void> {
  const look = worn.get(page);
  if (look) expect(await seen(page), `the browser still shows the look on “${screen}”`).toEqual(look);
  expect.soft(await settle(page), `“${screen}” settles`).toEqual([]);
  let { violations, incomplete } = await analyze(page);
  const solid = look?.transparency === 'reduce';
  if (solid) ({ violations, incomplete } = await inView(page, violations, incomplete));
  await test.info().attach(`axe: ${screen}`, { body: JSON.stringify({ violations, incomplete }, null, 2), contentType: 'application/json' });
  const undecided = solid ? report(incomplete, 'not decided by axe, with every material solid') : '';
  expect.soft([report(violations), undecided].filter(Boolean).join('\n\n'), `axe (WCAG 2.2 AA) on “${screen}”`).toBe('');
}

/**
 * Measures again each word whose contrast axe couldn't decide while the viewport's edge cut it, scrolled into the
 * middle of the viewport, then scrolls back. A word in a shadow root or a frame keeps its first answer.
 */
async function inView(page: Page, violations: Result[], incomplete: Result[]): Promise<{ violations: Result[]; incomplete: Result[] }> {
  const contrast = incomplete.find((r) => r.id === 'color-contrast');
  const out = { violations: [...violations], incomplete: incomplete.filter((r) => r !== contrast) };
  const scrolled = await page.evaluate(() => [scrollX, scrollY] as const);
  try {
    for (const node of contrast?.nodes ?? []) {
      const [selector, ...deeper] = node.target;
      const cut = typeof selector === 'string' && !deeper.length && await page.locator(selector).evaluate((el) => {
        const r = el.getBoundingClientRect();
        return r.top < 0 || r.left < 0 || r.bottom > innerHeight || r.right > innerWidth;
      });
      if (!cut) {
        out.incomplete.push({ ...(contrast as Result), nodes: [node] });
        continue;
      }
      await page.locator(selector).evaluate((el) => el.scrollIntoView({ block: 'center' }));
      expect.soft(await settle(page), `${selector} settles in view`).toEqual([]);
      const again = await new AxeBuilder({ page }).include(selector).options(axeOptions({ type: 'rule', values: ['color-contrast'] })).analyze();
      const same = (r: Result): Result => ({ ...r, nodes: r.nodes.filter((n) => n.target.length === 1 && n.target[0] === selector) });
      out.violations.push(...again.violations.map(same));
      out.incomplete.push(...again.incomplete.map(same));
    }
  } finally {
    await page.evaluate(([x, y]) => window.scrollTo(x, y), scrolled);
  }
  return { violations: merged(out.violations), incomplete: merged(out.incomplete) };
}

/** One result per rule, with every node it found. */
function merged(results: Result[]): Result[] {
  const by = new Map<string, Result>();
  for (const r of results.filter((r) => r.nodes.length)) {
    const had = by.get(r.id);
    by.set(r.id, had ? { ...had, nodes: [...had.nodes, ...r.nodes] } : r);
  }
  return [...by.values()];
}

/** axe's WCAG 2.2 AA results for the page as it is now, once its screen has settled. */
export async function audit(page: Page): Promise<AxeResults> {
  const unsettled = await settle(page);
  if (unsettled.length) throw new Error(unsettled.join('; '));
  return analyze(page);
}

async function analyze(page: Page): Promise<AxeResults> {
  // The design system's glass draws its specular rim as an ::after over the whole card, masked to a 1px edge, and axe
  // gives up ("incomplete", which passes) on every word above a large pseudo-element. The rim paints nothing under the
  // words, so contrast is measured as if pseudo-elements weren't there: a word over a pseudo-element's fill was never
  // measured before either. Glass itself (a blur over the wallpaper) stays beyond axe: it measures the card under
  // reduced transparency, where every material is solid. (axe merges these check options into its defaults; its types
  // omit `checks`.) AxeBuilder's options() replaces what withTags() set, so the tags go in here.
  return new AxeBuilder({ page }).options(axeOptions({ type: 'tag', values: WCAG22AA })).analyze();
}

const axeOptions = (runOnly: { type: 'tag' | 'rule'; values: string[] }): RunOptions => ({
  runOnly,
  rules: runOnly.type === 'tag' ? Object.fromEntries(EXPERIMENTAL_WCAG.map((id) => [id, { enabled: true }])) : {},
  checks: { 'color-contrast': { options: { ignorePseudo: true } } },
} as RunOptions);

/**
 * Waits for the fonts, every finite animation (view transitions, entrances, the theme's glide), and then the layout:
 * a disclosure's ::details-content grows by a transition that getAnimations() doesn't list, so the boxes' layout (not
 * their transforms, which endless animations keep moving) must hold still for a few frames. Returns what never
 * settled, if anything.
 */
async function settle(page: Page): Promise<string[]> {
  return page.evaluate(async () => {
    const problems: string[] = [];
    const frame = () => new Promise((r) => requestAnimationFrame(r));
    await document.fonts.ready;
    for (let i = 0; ; i++) {
      const running = document.getAnimations().filter((a) => a.playState === 'running' && Number.isFinite(a.effect?.getComputedTiming().endTime ?? Infinity));
      if (!running.length) break;
      if (i === 5) {
        const name = (a: Animation) => a.id || (a as CSSAnimation).animationName || (a as CSSTransition).transitionProperty || a.constructor.name;
        problems.push(`animations still run after 5 rounds: ${running.map(name).join(', ')}`);
        break;
      }
      await Promise.allSettled(running.map((a) => a.finished));
    }
    const layout = () => {
      const boxes = [...document.body.querySelectorAll('*')].filter((e) => e instanceof HTMLElement);
      return boxes.map((e) => `${e.offsetLeft},${e.offsetTop},${e.offsetWidth},${e.offsetHeight}`).join(' ');
    };
    let last = layout();
    for (let still = 0, i = 0; still < 3; i++) {
      if (i === 300) {
        problems.push('the layout still moves after 300 frames');
        break;
      }
      await frame();
      const now = layout();
      still = now === last ? still + 1 : 0;
      last = now;
    }
    return problems;
  });
}

/** One paragraph per rule: its impact (or `why`), its help text and link, then each element with axe's reasons. */
export function report(rules: Result[], why?: string): string {
  return rules.map((v) => [
    `${v.id} (${why ?? v.impact ?? 'unknown impact'}): ${v.help}`,
    `  ${v.helpUrl}`,
    ...v.nodes.flatMap((n) => [
      `  - ${n.target.join(' ')}`,
      ...(n.failureSummary ?? '').split('\n').filter((s) => s.trim()).map((s) => `      ${s.trim()}`),
    ]),
  ].join('\n')).join('\n\n');
}
