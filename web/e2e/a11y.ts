// Accessibility checks: axe-core's WCAG 2.2 AA rules on a surface's screens, as a person's device shows them, and the
// contrast of every word against the pixels behind it. A test takes one look (a theme, and whether motion and
// transparency are reduced), opens its surface in it, and calls `accessible(page, 'name of the screen')` once per
// screen it reaches (with the glass levels the surface offers, if it offers any). Any violation fails the test (and,
// with every material solid, anything axe couldn't decide), with a report that names the look, the screen, the rule and
// each element; axe's findings and the contrasts measured are attached to the HTML report.
import AxeBuilder from '@axe-core/playwright';
import { expect, type Page, test } from '@playwright/test';
import { boxContrast, COVERED, decodePng, glyphsShow, hideWords, needed, PATCH, refade, type Shot, type TextRun, textRuns, wordBox } from './pixels';

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

/** The design system's glass levels (data-glass on <html>): Clear, Balanced (the default), Frosted, and Solid. */
export const glassLevels = ['clear', 'balanced', 'frosted', 'solid'] as const;
export type Glass = (typeof glassLevels)[number];

/**
 * Runs axe's WCAG 2.2 AA rules on the page as it is now, once its screen has settled, and fails the test (softly, so
 * the test goes on to its next screens) on any violation. Under reduced transparency every material is solid, so
 * whatever axe couldn't decide fails too, once each undecided word the viewport's edge cuts has been measured again in
 * view. Then it measures every word against the pixels behind it (see legible()), at each of `glass`, the levels the
 * surface offers, or at the page's own level when it offers none, and fails on any word below AA. What axe found or
 * couldn't decide, and the contrasts measured, are attached to the report.
 */
export async function accessible(page: Page, screen: string, { glass }: { glass?: readonly Glass[] } = {}): Promise<void> {
  const look = worn.get(page);
  if (look) expect(await seen(page), `the browser still shows the look on “${screen}”`).toEqual(look);
  expect.soft(await settle(page), `“${screen}” settles`).toEqual([]);
  let { violations, incomplete } = await analyze(page);
  const solid = look?.transparency === 'reduce';
  if (solid) ({ violations, incomplete } = await inView(page, violations, incomplete));
  await test.info().attach(`axe: ${screen}`, { body: JSON.stringify({ violations, incomplete }, null, 2), contentType: 'application/json' });
  const undecided = solid ? report(incomplete, 'not decided by axe, with every material solid') : '';
  expect.soft([report(violations), undecided].filter(Boolean).join('\n\n'), `axe (WCAG 2.2 AA) on “${screen}”`).toBe('');
  for (const level of glass ?? [undefined]) {
    const { runs, problems } = await legible(page, level);
    const at = `${look ? lookName(look) : 'the browser’s own look'}, ${problems.level}`;
    await test.info().attach(`pixels: ${screen} (${at})`, { body: JSON.stringify(runs, null, 2), contentType: 'application/json' });
    expect.soft(problems.text ? `${at}:\n${problems.text}` : '', `contrast against the pixels (WCAG 2.2 AA) on “${screen}”`).toBe('');
  }
}

/**
 * What legible() measured of one run of text: its worst word's contrast (NaN when none could be measured), what it
 * needs, and why any of its words couldn't be measured.
 */
export type Measured = { where: string; text: string; size: number; weight: number; contrast: number; needs: number; unmeasured: string[] };

/**
 * Measures each run of text the page shows against the pixels behind it, at glass level `glass` (or the page's own):
 * - it finds each word's boxes (Range.getClientRects, clipped as the page clips them);
 * - it hides every word's glyphs and takes one screenshot of the area the words in view cover, so the shot holds what
 *   is behind them: glass, its blur, the wallpaper;
 * - each word's contrast is its ink (from its computed style) alpha-composited over each pixel of its box, against that
 *   pixel; the box's worst patch over a tenth of it each way decides (pixels.ts says why); a word in a faded group is composited inside the
 *   group, from a second shot with the group opaque;
 * - a run's contrast is its worst word's, and it fails below 4.5:1 (3:1 for large text).
 * Words outside the viewport, under a scroller's edge or covered (by a sticky bar, say) are scrolled to the middle and
 * shot again there (one shot per scroll, for every word it shows), then everything is scrolled back. A word that can't
 * be brought wholly into view, that something still covers there, or whose ink isn't one colour, fails as not measured.
 */
export async function legible(page: Page, glass?: Glass): Promise<{ runs: Measured[]; problems: { level: string; text: string } }> {
  const before = await page.evaluate((g) => {
    const html = document.documentElement;
    const was = html.getAttribute('data-glass');
    if (g) html.setAttribute('data-glass', g);
    return was;
  }, glass ?? null);
  try {
    if (glass) expect.soft(await settle(page), `the page settles at glass level ${glass}`).toEqual([]);
    const [level, reduced] = await page.evaluate(() => [getComputedStyle(document.documentElement).getPropertyValue('--glass').trim(),
      matchMedia('(prefers-reduced-transparency: reduce)').matches] as const);
    const own = glassLevels.find((l) => l === before);
    const preset = glass ?? own ?? ({ 0: 'clear', 0.5: 'balanced', 1: 'frosted' } as Record<string, Glass | undefined>)[level];
    // reduced transparency makes every material solid, whatever the level
    const levelName = reduced ? 'glass Solid (reduced transparency)' : !level ? 'no glass level'
      : `glass ${preset ? preset[0]?.toUpperCase() + preset.slice(1) : 'of its own'} (--glass: ${level})`;
    await page.evaluate(hideWords, 'hide' as const);
    let { runs, signature } = await page.evaluate(textRuns, null);
    if (!runs.length) throw new Error('no words found on the page');
    const first = runs;
    const contrast = new Map<string, number>();
    const unmeasured = new Map<string, string>();
    const pending = new Set(first.flatMap((r) => r.boxes.map((b) => b.key)));
    for (const r of first) {
      if (!r.unreadable) continue;
      for (const b of r.boxes) unmeasured.set(b.key, r.unreadable);
      for (const b of r.boxes) pending.delete(b.key);
    }
    let target: string | undefined;
    for (;;) {
      const now = runs.flatMap((r) => r.boxes.filter((b) => b.inView && pending.has(b.key)).map((b) => [r, b] as const));
      if (now.length) {
        // one shot of the area these words cover, in CSS pixels: behind hidden words the background is smooth (glass
        // blurs, the wallpaper is gradients), so the device's extra pixels add time and nothing else
        const [left, top] = [Math.floor(Math.min(...now.map(([, b]) => b.x))), Math.floor(Math.min(...now.map(([, b]) => b.y)))];
        const clip = { x: left, y: top, width: Math.ceil(Math.max(...now.map(([, b]) => b.x + b.w))) - left, height: Math.ceil(Math.max(...now.map(([, b]) => b.y + b.h))) - top };
        const shoot = async () => decodePng(await page.screenshot({ scale: 'css', animations: 'allow', clip }));
        const shot = await shoot();
        const faded = [...new Set(now.filter(([r]) => r.fade < 1).map(([r]) => r.id))];
        let unfaded: Shot | undefined;
        if (faded.length) {
          try {
            await page.evaluate(textRuns, { unfade: faded });
            unfaded = await shoot();
          } finally {
            await page.evaluate(refade);
          }
        }
        // the same area with every glyph in the sentinel colour: a word whose glyphs don't show is covered, by whatever
        await page.evaluate(hideWords, 'sentinel' as const);
        let marked: Shot;
        try {
          marked = await shoot();
        } finally {
          await page.evaluate(hideWords, 'hide' as const);
        }
        // every word's box where it is now, in the shot: their glyphs (stroked in the sentinel) are not each other's
        const words = runs.flatMap((r) => r.boxes.filter(wordBox).map((b) => ({ ...b, x: b.x - left, y: b.y - top })));
        for (const [r, b] of now) {
          const at = { ...b, x: b.x - left, y: b.y - top };
          const shows = glyphsShow(shot, marked, at, r.fade, shot.width / clip.width, r.fade < 1 ? unfaded : undefined, words.filter((w) => w.key !== b.key));
          if (shows !== true) {
            unmeasured.set(b.key, shows === null ? 'on a background near the sentinel’s colour, so nobody can tell whether its glyphs show' : COVERED);
            pending.delete(b.key);
            continue;
          }
          const c = boxContrast(shot, { ...b, x: b.x - left, y: b.y - top }, r.ink, r.fade, shot.width / clip.width, r.fade < 1 ? unfaded : undefined);
          if (Number.isNaN(c)) unmeasured.set(b.key, 'too small to hold a pixel');
          else contrast.set(b.key, c);
          pending.delete(b.key);
        }
      }
      if (target && pending.delete(target)) {
        // scrolled to the middle, and still not wholly in view, or covered there too
        const box = runs.flatMap((r) => r.boxes).find((b) => b.key === target);
        unmeasured.set(target, box?.covered ? `covered by ${box.covered}` : 'never wholly in view');
      }
      target = first.flatMap((r) => r.boxes).find((b) => pending.has(b.key))?.key;
      if (!target) break;
      await page.evaluate(textRuns, { scrollTo: target });
      expect.soft(await settle(page), 'the page settles where a word was scrolled to').toEqual([]);
      const again = await page.evaluate(textRuns, null);
      if (again.signature !== signature) throw new Error('the page changed while its words were measured');
      ({ runs, signature } = again);
    }
    const measured = first.map((r: TextRun): Measured => {
      const found = r.boxes.map((b) => contrast.get(b.key)).filter((c): c is number => c !== undefined);
      return { where: r.where, text: r.text, size: r.size, weight: r.weight, contrast: found.length ? Math.min(...found) : NaN,
        needs: needed(r.size, r.weight), unmeasured: r.boxes.flatMap((b) => unmeasured.get(b.key) ?? []) };
    });
    const excerpt = (t: string) => (t.length > 60 ? `${t.slice(0, 59)}…` : t);
    const font = (m: Measured) => `(${m.size}px, weight ${m.weight}; its worst word’s worst patch, over ${PATCH * 100}% of the box each way)`;
    const why = (m: Measured) => {
      const n = m.unmeasured.length;
      return `not measured, ${n} ${n === 1 ? 'word' : 'words'}: ${[...new Set(m.unmeasured)].join('; ')}`;
    };
    const lines = measured.flatMap((m) => [
      ...(m.contrast < m.needs ? [`  - ${m.where} “${excerpt(m.text)}”: ${m.contrast.toFixed(2)}:1, needs ${m.needs}:1 ${font(m)}`] : []),
      ...(m.unmeasured.length ? [`  - ${m.where} “${excerpt(m.text)}”: ${why(m)}`] : []),
    ]);
    return { runs: measured, problems: { level: levelName, text: lines.join('\n') } };
  } finally {
    await page.evaluate(hideWords, 'show' as const);
    await unscroll(page);
    await page.evaluate((was) => {
      const html = document.documentElement;
      if (was === null) html.removeAttribute('data-glass');
      else html.setAttribute('data-glass', was);
    }, before);
    if (glass) expect.soft(await settle(page), 'the page settles back at its own glass level').toEqual([]);
  }
}

/**
 * Measures again each word whose contrast axe couldn't decide while the viewport's edge cut it, scrolled into the
 * middle of the viewport, then scrolls everything it moved back. A word in a shadow root or a frame keeps its first
 * answer, and so does a word the second run doesn't mention at all: it stays undecided, never a pass.
 */
async function inView(page: Page, violations: Result[], incomplete: Result[]): Promise<{ violations: Result[]; incomplete: Result[] }> {
  const contrast = incomplete.find((r) => r.id === 'color-contrast');
  const out = { violations: [...violations], incomplete: incomplete.filter((r) => r !== contrast) };
  const undecided = (node: Result['nodes'][number]) => out.incomplete.push({ ...(contrast as Result), nodes: [node] });
  try {
    for (const node of contrast?.nodes ?? []) {
      const [selector, ...deeper] = node.target;
      const cut = typeof selector === 'string' && !deeper.length && await page.locator(selector).evaluate((el) => {
        const r = el.getBoundingClientRect();
        return r.top < 0 || r.left < 0 || r.bottom > innerHeight || r.right > innerWidth;
      });
      if (!cut) {
        undecided(node);
        continue;
      }
      await page.locator(selector).evaluate((el) => {
        // every box that scrolls, from the word's parent up to the document, with where it was
        const w = window as { a11yScrolled?: [Element, number, number][] };
        const was = (w.a11yScrolled ??= []);
        for (let e = el.parentElement; e; e = e.parentElement) {
          if (e.scrollHeight > e.clientHeight || e.scrollWidth > e.clientWidth) was.push([e, e.scrollLeft, e.scrollTop]);
        }
        const doc = document.scrollingElement;
        if (doc) was.push([doc, doc.scrollLeft, doc.scrollTop]);
        el.scrollIntoView({ block: 'center' });
      });
      expect.soft(await settle(page), `${selector} settles in view`).toEqual([]);
      const again = await new AxeBuilder({ page }).include(selector).options(axeOptions({ type: 'rule', values: ['color-contrast'] })).analyze();
      const same = (r: Result): Result => ({ ...r, nodes: r.nodes.filter((n) => n.target.length === 1 && n.target[0] === selector) });
      const mine = (rs: Result[]) => rs.map(same).filter((r) => r.nodes.length);
      const [v, i, p] = [mine(again.violations), mine(again.incomplete), mine(again.passes)];
      if (!v.length && !i.length && !p.length) {
        undecided(node);
        continue;
      }
      out.violations.push(...v);
      out.incomplete.push(...i);
    }
  } finally {
    await unscroll(page);
  }
  return { violations: merged(out.violations), incomplete: merged(out.incomplete) };
}

/** Puts back every scroll position noted in `a11yScrolled` (inView and textRuns note them before they scroll). */
async function unscroll(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as { a11yScrolled?: [Element, number, number][] };
    for (const [e, left, top] of (w.a11yScrolled ?? []).reverse()) e.scrollTo({ left, top, behavior: 'instant' }); // the first record of each wins
    delete w.a11yScrolled;
  });
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
