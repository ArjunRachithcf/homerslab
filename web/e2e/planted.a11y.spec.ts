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
// sheet: rules for what the CSSOM's inline style can't reach (pseudo-elements), in a constructed style sheet
// scroll: [selector, top]: that box scrolled so far down
const plants: {
  name: string; look: Look; css?: [string, string, string][]; html?: string; after?: string; sheet?: string; scroll?: [string, number];
  cut?: boolean; hideOnScroll?: boolean;
}[] = [
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
  // a faint label floating over a field, which pointer-events: none lets clicks through: still measured, not covered
  { name: 'a faint label floating over a field', look: { theme: 'light', motion: 'reduce', transparency: 'no-preference' },
    html: '<div id="planted-field"><input id="planted-input" aria-label="Planted"><span id="planted-float">A floating label</span></div>',
    css: [['#planted-field', 'position', 'relative'], ['#planted-field', 'text-align', 'left'], ['#planted-float', 'position', 'absolute'], ['#planted-float', 'left', '8px'],
      ['#planted-float', 'top', '2px'], ['#planted-float', 'pointer-events', 'none'], ['#planted-float', 'color', '#b4b4b4']] },
  // a fixed toast escapes the overflow of the box it sits in, when that box doesn't hold fixed boxes: still measured
  { name: 'a faint toast in a box that hides its overflow', look: { theme: 'light', motion: 'reduce', transparency: 'no-preference' },
    html: '<div id="planted-wrap"><p id="planted-toast">A faint toast</p></div>', after: '.as-foot',
    css: [['#planted-wrap', 'overflow', 'hidden'], ['#planted-wrap', 'height', '0'], ['#planted-toast', 'position', 'fixed'],
      ['#planted-toast', 'left', '24px'], ['#planted-toast', 'top', '30%'], ['#planted-toast', 'color', '#a8a8a8']] },
  // dark words in a faded grey group: inside the group they sit on the grey, which fades with them (3.3:1 on screen;
  // fading only the ink would give 5.1:1)
  { name: 'dark words in a faded grey group', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<p id="planted-dim">Dark words on a faded grey</p>',
    css: [['#planted-dim', 'background-color', '#787878'], ['#planted-dim', 'color', '#000'], ['#planted-dim', 'opacity', '0.7']] },
  // a faint word under an opaque bar inside the glass card: the card's rim (an ::after over all of it) doesn't hide
  // what covers the word
  { name: 'a faint word under an opaque bar in the card', look: { theme: 'light', motion: 'reduce', transparency: 'no-preference' },
    html: '<div id="planted-barbox"><p id="planted-under">Hidden under a bar</p><div id="planted-bar"></div></div>',
    css: [['#planted-barbox', 'position', 'relative'], ['#planted-bar', 'position', 'absolute'], ['#planted-bar', 'inset', '0'],
      ['#planted-bar', 'background-color', '#222'], ['#planted-under', 'color', '#d0d0d0']] },
  // a faint word in a scrolling box, scrolled under its sticky header: measured once scrolled out from under it, not
  // against the header
  { name: 'a faint word in a scrolling box under a sticky header', look: { theme: 'light', motion: 'reduce', transparency: 'no-preference' },
    html: '<div id="planted-scroller"><div id="planted-sticky">A header</div><p id="planted-scrolled">A faint scrolled word</p><div id="planted-room"></div></div>',
    css: [['#planted-scroller', 'height', '120px'], ['#planted-scroller', 'overflow', 'auto'], ['#planted-scroller', 'background-color', '#fff'],
      ['#planted-sticky', 'position', 'sticky'], ['#planted-sticky', 'top', '0'], ['#planted-sticky', 'height', '40px'],
      ['#planted-sticky', 'background-color', '#222'], ['#planted-sticky', 'color', '#fff'], ['#planted-scrolled', 'margin', '0'],
      ['#planted-scrolled', 'color', '#c4c4c4'], ['#planted-room', 'height', '300px']],
    scroll: ['#planted-scroller', 40] },
  // a faint word in a strip that snaps (Admin's screenshots): the strip's snap points are its ends, so scrolling a word
  // to the middle snaps back and leaves it half off the strip's edge; it is measured all the same, snap or not
  { name: 'a faint word in a strip that snaps', look: { theme: 'light', motion: 'reduce', transparency: 'no-preference' },
    html: '<div id="planted-strip"><p id="planted-snapped">Alpha beta gamma delta</p><p id="planted-snapped-faint">Alpha beta gamma delta</p></div>',
    css: [['#planted-strip', 'width', '240px'], ['#planted-strip', 'overflow-x', 'auto'], ['#planted-strip', 'display', 'grid'],
      ['#planted-strip', 'grid-auto-flow', 'column'], ['#planted-strip', 'grid-auto-columns', '200px'], ['#planted-strip', 'column-gap', '16px'], 
      ['#planted-strip', 'background-color', '#fff'], ['#planted-snapped', 'color', '#000'], ['#planted-snapped-faint', 'color', '#c4c4c4']],
    sheet: '#planted-strip { scroll-snap-type: x mandatory; } #planted-strip p { margin: 0; white-space: nowrap; scroll-snap-align: start; }' },
  // what fields show: a select's option, a button's value
  { name: 'a faint option in a select', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<select id="planted-select" aria-label="Planted"><option>A faint option</option></select>',
    css: [['#planted-select', 'color', '#c8c8c8']] },
  { name: 'a faint submit button’s value', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<input id="planted-submit" type="submit" value="A faint value">',
    css: [['#planted-submit', 'color', '#c8c8c8']] },
  // words the check can't measure fail as not measured, faint or not
  { name: 'words drawn by CSS content', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<p id="planted-gen"></p>', sheet: '#planted-gen::before { content: "Generated words"; }' },
  { name: 'words painted through background-clip: text', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<p id="planted-clip">Clipped words</p>',
    sheet: '#planted-clip { background-image: linear-gradient(#ccc, #ddd); -webkit-background-clip: text; background-clip: text; color: transparent; }' },
  { name: 'a first letter in its own colour', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<p id="planted-first">First letter</p>', sheet: '#planted-first::first-letter { color: #ddd; }' },
  { name: 'words under a filter', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<div id="planted-filter"><p>Filtered words</p></div>', css: [['#planted-filter', 'filter', 'brightness(1.8)']] },
  { name: 'words that blend', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<p id="planted-blend">Blended words</p>', css: [['#planted-blend', 'mix-blend-mode', 'difference']] },
  { name: 'words drawn by CSS content on an inline box', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<p>Read <span id="planted-gen-inline"></span></p>', sheet: '#planted-gen-inline::before { content: "the guide"; }' },
  { name: 'a first line in its own colour', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<p id="planted-firstline">First line</p>', sheet: '#planted-firstline::first-line { color: #ddd; }' },
  { name: 'masked words', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<div id="planted-mask"><p>Masked words</p></div>', sheet: '#planted-mask { mask-image: linear-gradient(90deg, #000, transparent); }' },
  // a dark band 10px wide under a long word: a patch as wide as a tenth of the word would step over it
  { name: 'a word over a narrow dark band', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<p id="planted-band">Configurations</p>',
    css: [['#planted-band', 'color', '#333'], ['#planted-band', 'font-size', '16px'], ['#planted-band', 'display', 'inline-block'],
      ['#planted-band', 'background-image', 'linear-gradient(90deg, #fff 60px, #444 60px 70px, #fff 70px)']] },
  // a word under its own ancestor's ::after: no hit test sees that cover (the ancestor's paint is the background), but
  // the word's glyphs don't show
  { name: 'a faint word under its box’s ::after', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<div id="planted-box"><p id="planted-boxed">Under a pseudo bar</p></div>',
    sheet: '#planted-box { position: relative; } #planted-box::after { content: ""; position: absolute; inset: 0; background: #222; pointer-events: none; }',
    css: [['#planted-boxed', 'color', '#d0d0d0']] },
  // black words under a cover that lets 40% of them through: #999 on white on the screen, 2.85:1, where words
  // measured as uncovered read 21:1. Not measured: what shows through can't be told from the shots
  ...([['white at 60%', 'background: rgba(255, 255, 255, 0.6);'], ['white at opacity 0.6', 'background: #fff; opacity: 0.6;'],
    ['#999 that screens', 'background: #999; mix-blend-mode: screen;']] as const).map(([cover, rule], i) => ({
    name: `black words under ${cover}`, look: { theme: 'light', motion: 'reduce', transparency: 'reduce' } as Look,
    html: `<div id="planted-veil-${i}"><p id="planted-veiled-${i}">Under a veil</p></div>`,
    sheet: `#planted-veil-${i} { position: relative; background: #fff; } #planted-veil-${i}::after { content: ""; position: absolute; inset: 0; ${rule} pointer-events: none; }`,
    css: [[`#planted-veiled-${i}`, 'color', '#000']] as [string, string, string][] })),
  // ::first-line and ::first-letter on a block around the words' own: the outer block's first line is the inner's
  // (a hidden text before it starts no line)
  { name: 'a first line in its own colour, set on the block around', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<div id="planted-outer-line"><p hidden>Hidden</p><p id="planted-inner-line">First line inside</p></div>', sheet: '#planted-outer-line::first-line { color: #eee; }' },
  // the first letter takes the punctuation before it, here a text of its own
  { name: 'a first letter in its own colour, after a quotation mark', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<p id="planted-quote">“<span id="planted-quoted">Quoted</span> words”</p>', sheet: '#planted-quote::first-letter { color: #eee; }' },
  { name: 'a first letter in its own colour, set on the block around', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<div id="planted-outer-letter"><p hidden>Hidden</p><div><p id="planted-inner-letter">First letter inside</p></div></div>', sheet: '#planted-outer-letter::first-letter { color: #eee; }' },
  // a line under an opaque white ::after, set solid under a line whose descenders reach into its box: they show
  // there, but they aren't its glyphs
  { name: 'words under a cover, below descenders', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<div id="planted-tight"><p id="planted-descenders">gypsy jiggy query</p><p id="planted-below">Covered below</p></div>',
    sheet: '#planted-tight p { margin: 0; line-height: 1; color: #000; } #planted-below { position: relative; } ' +
      '#planted-below::after { content: ""; position: absolute; inset: 0; background: #fff; pointer-events: none; }' },
  // a badge out of the flow, above the block, comes first in it but starts no line: the words after it do
  ...(['first-line', 'first-letter'] as const).map((pseudo) => ({
    name: `a ${pseudo.replace('-', ' ')} in its own colour, after a badge out of the flow`, look: { theme: 'light', motion: 'reduce', transparency: 'reduce' } as Look,
    html: `<div id="planted-badged-${pseudo}"><span class="badge">Badge</span><p id="planted-after-badge-${pseudo}">Words after a badge</p></div>`,
    sheet: `#planted-badged-${pseudo} { position: relative; margin-top: 60px; } #planted-badged-${pseudo} .badge { position: absolute; top: -40px; left: 0; } ` +
      `#planted-badged-${pseudo}::${pseudo} { color: #eee; }` })),
  // large text needs 3:1, and gets no more slack: 3.2:1 passes at 24px (setup.a11y.spec.ts), and fails at 18px
  { name: 'words at 3.2:1 below large', look: { theme: 'light', motion: 'reduce', transparency: 'reduce' },
    html: '<p id="planted-small">Not quite large</p>', css: [['#planted-small', 'font-size', '18px'], ['#planted-small', 'color', '#909090']] }, // 3.19:1 on white
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
    if (plant.sheet) {
      await page.evaluate((rules) => {
        const sheet = new CSSStyleSheet();
        sheet.replaceSync(rules);
        document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
      }, plant.sheet);
    }
    const scroll = plant.scroll;
    if (scroll) await page.locator(scroll[0]).evaluate((el, top) => { el.scrollTop = top; }, scroll[1]);
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
