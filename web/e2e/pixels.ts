// Contrast against the pixels: what a word's contrast is on the screen itself, glass, blur and wallpaper included,
// where axe can only read colours from the style sheets. a11y.ts's accessible() runs it on every screen; this file has
// its parts: what runs in the page (finding each word's boxes, hiding the words), reading a screenshot, and the sums.
import { inflateSync } from 'node:zlib';

/**
 * Which of a box's background pixels decide its contrast: the worst patch of them that is more than a tenth of the box
 * wide and more than a tenth of it high (the patch's contrast is its best pixel's). A line across the box no thicker
 * than a tenth of it (the glass's rim, a field's border, a focus ring, a 1px rule through 16px words) decides nothing;
 * a light patch under part of a word that size or larger, a few letters wide and a stroke or two high, decides it.
 */
export const PATCH = 0.1;

/** WCAG 2.2's AA minimum (1.4.3): 4.5:1, or 3:1 for large text (18pt, 24px and up; or 14pt, 56/3 px and up, in bold). */
export const needed = (size: number, weight: number): number => (size >= 24 || (size >= 56 / 3 && weight >= 700) ? 3 : 4.5);

/**
 * A word's box on the screen, in CSS pixels from the viewport's corner. `inView` once the viewport and every scroller
 * show all of it and nothing covers its middle; `covered` names what does, when something does.
 */
export type Box = { key: string; x: number; y: number; w: number; h: number; inView: boolean; covered: string | null };

/**
 * A run of text: a text node (or a field's value or placeholder) with its ink (sRGB 0-255, alpha 0-1), `fade` (its
 * element's and every ancestor's opacity, multiplied) and its boxes. `unreadable` says why its ink can't be measured.
 */
export type TextRun = {
  id: number; where: string; text: string; size: number; weight: number; ink: [number, number, number, number]; fade: number;
  unreadable?: string; boxes: Box[];
};

/** What the page shows: its runs of text, and a fingerprint of all its text, to tell when the page has changed. */
export type Words = { runs: TextRun[]; signature: string };

/**
 * In the page, with no task: every run of text it shows, with a box per word (per line, when a word breaks across
 * two). Text that isn't shown (hidden, transparent, clipped away, collapsed) has no boxes; nor has text in an inactive
 * control, which WCAG exempts, or text that a modal dialog has made inert. With a task:
 * - `scrollTo` (a box's key): scrolls that box to the middle of its scrollers and the viewport, noting every scroll
 *   position it changes in `a11yScrolled` for a11y.ts to put back;
 * - `unfade` (run ids): sets every ancestor of those runs that is less than opaque to opacity 1, noting the old values
 *   in `a11yUnfaded` for refade(), so a shot shows what their words are painted on inside the faded group.
 */
export function textRuns(task: { scrollTo?: string; unfade?: number[] } | null): Words {
  type Edges = { l: number; t: number; r: number; b: number };
  const w = window as { a11yHidden?: CSSStyleSheet; a11yScrolled?: [Element, number, number][]; a11yUnfaded?: [HTMLElement | SVGElement, string, string][] };
  // the inks as the page paints them: hideWords' sheet off while they are read; and every element hit-testable, so
  // that a word under pointer-events: none is still found where it is (all in one task, so nothing is painted)
  const hidden = w.a11yHidden;
  const hits = new CSSStyleSheet();
  hits.replaceSync('* { pointer-events: auto !important; }'); // elements only: the glass's rim is an ::after over all the card
  const roots: (Document | ShadowRoot)[] = [document];
  type Field = HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;
  const found: (Text | Field)[] = [];
  const elements: Element[] = [];
  const walk = (root: Node) => {
    const tw = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT);
    for (let n = tw.nextNode(); n; n = tw.nextNode()) {
      if (n instanceof Text) {
        if (/\S/.test(n.data)) found.push(n);
      } else if (n instanceof HTMLTextAreaElement || n instanceof HTMLSelectElement ||
        (n instanceof HTMLInputElement && /^(text|search|email|url|tel|number|password|submit|button|reset)$/.test(n.type))) {
        found.push(n);
      }
      if (n instanceof Element) elements.push(n);
      if (n instanceof Element && n.shadowRoot) {
        roots.push(n.shadowRoot);
        walk(n.shadowRoot);
      }
    }
  };
  walk(document.body);
  if (hidden) hidden.disabled = true;
  for (const r of roots) r.adoptedStyleSheets = [...r.adoptedStyleSheets, hits];
  // an inline !important beats the sheet: those elements are set inline too, and put back
  const inline: [HTMLElement | SVGElement, string, string][] = [];
  for (const e of elements) {
    if ((e instanceof HTMLElement || e instanceof SVGElement) && getComputedStyle(e).pointerEvents === 'none') {
      inline.push([e, e.style.getPropertyValue('pointer-events'), e.style.getPropertyPriority('pointer-events')]);
      e.style.setProperty('pointer-events', 'auto', 'important');
    }
  }
  try {
    return collect();
  } finally {
    for (const [e, value, priority] of inline) {
      if (value) e.style.setProperty('pointer-events', value, priority);
      else e.style.removeProperty('pointer-events');
    }
    for (const r of roots) r.adoptedStyleSheets = r.adoptedStyleSheets.filter((s) => s !== hits);
    if (hidden) hidden.disabled = false;
  }

  function collect(): Words {
    const up = (e: Element): Element | null => e.parentElement ?? ((e.parentNode as ShadowRoot | null)?.host ?? null);
    const cut = (a: Edges, b: Edges): Edges => ({ l: Math.max(a.l, b.l), t: Math.max(a.t, b.t), r: Math.min(a.r, b.r), b: Math.min(a.b, b.b) });
    const inner = (e: Element): Edges => {
      const r = e.getBoundingClientRect();
      return { l: r.left + e.clientLeft, t: r.top + e.clientTop, r: r.left + e.clientLeft + e.clientWidth, b: r.top + e.clientTop + e.clientHeight };
    };
    const style = new Map<Element, CSSStyleDeclaration>();
    const css = (e: Element) => {
      let s = style.get(e);
      if (!s) style.set(e, (s = getComputedStyle(e)));
      return s;
    };

    // what clips an element's words: its own overflow, then its containing block's (a box positioned absolutely or
    // fixed escapes the overflow of every ancestor in between). Overflow that hides clips for good; a scroller shows
    // its words once scrolled to.
    const holdsFixed = (s: CSSStyleDeclaration) => s.transform !== 'none' || s.filter !== 'none' || s.backdropFilter !== 'none' ||
      s.perspective !== 'none' || /paint|layout|strict|content/.test(s.contain) || /transform|filter|perspective/.test(s.willChange) ||
      s.containerType !== 'normal';
    const containingBlock = (el: Element): Element | null => {
      const position = css(el).position;
      let e = up(el);
      if (position === 'fixed') while (e && !holdsFixed(css(e))) e = up(e);
      if (position === 'absolute') while (e && css(e).position === 'static' && !holdsFixed(css(e))) e = up(e);
      return e;
    };
    type Clips = { hard: Edges[]; scrollers: Element[] };
    const clipCache = new Map<Element, Clips>();
    const clips = (el: Element): Clips => {
      let c = clipCache.get(el);
      if (c) return c;
      const block = containingBlock(el);
      const above: Clips = block ? clips(block) : { hard: [], scrollers: [] };
      const s = css(el);
      c = above;
      if (s.display !== 'inline' && s.display !== 'contents' && (s.overflowX !== 'visible' || s.overflowY !== 'visible')) {
        const scrolls = /auto|scroll/.test(s.overflowX + s.overflowY);
        c = scrolls ? { hard: above.hard, scrollers: [...above.scrollers, el] } : { hard: [...above.hard, inner(el)], scrollers: above.scrollers };
      }
      clipCache.set(el, c);
      return c;
    };
    // a filter or a blend on the element or an ancestor changes what its words look like over what is below them
    const effectCache = new Map<Element, string | null>();
    const effect = (e: Element | null): string | null => {
      if (!e) return null;
      let f = effectCache.get(e);
      if (f === undefined) {
        const s = css(e);
        f = s.filter !== 'none' ? `under a filter (${where(e)})` : s.mixBlendMode !== 'normal' ? `blended with what is below (${where(e)})` : effect(up(e));
        effectCache.set(e, f);
      }
      return f;
    };
    // what covers a point of an element's words: anything above it there that isn't one of its ancestors (the
    // ancestors' own paint is the background being measured). Hit-tested with every element taking pointer events.
    const coveredBy = (el: Element, root: Document | ShadowRoot, x: number, y: number): string | null => {
      for (const e of root.elementsFromPoint(x, y)) {
        if (e === el || el.contains(e)) return null;
        let mine = false;
        for (let a = up(el); a && !mine; a = up(a)) mine = a === e;
        if (!mine) return where(e);
      }
      return 'nothing it can find';
    };
    const fadeCache = new Map<Element, number>();
    const fadeOf = (e: Element | null): number => {
      if (!e) return 1;
      let f = fadeCache.get(e);
      if (f === undefined) fadeCache.set(e, (f = Number(css(e).opacity) * fadeOf(up(e))));
      return f;
    };

    const canvas = new OffscreenCanvas(1, 1).getContext('2d', { willReadFrequently: true });
    const rgba = (colour: string): [number, number, number, number] | null => {
      const legacy = /^rgba?\(([\d.]+), ([\d.]+), ([\d.]+)(?:, ([\d.]+))?\)$/.exec(colour);
      if (legacy) return [Number(legacy[1]), Number(legacy[2]), Number(legacy[3]), legacy[4] === undefined ? 1 : Number(legacy[4])];
      const srgb = /^color\(srgb ([\d.e-]+) ([\d.e-]+) ([\d.e-]+)(?: \/ ([\d.]+))?\)$/.exec(colour);
      if (srgb) return [Number(srgb[1]) * 255, Number(srgb[2]) * 255, Number(srgb[3]) * 255, srgb[4] === undefined ? 1 : Number(srgb[4])];
      // any other colour space: as the browser paints it (a canvas ignores what isn't a colour: the sentinel stays)
      const sentinel = '#010203';
      if (!canvas) return null;
      canvas.fillStyle = sentinel;
      canvas.fillStyle = colour;
      if (canvas.fillStyle === sentinel && colour !== sentinel) return null;
      canvas.clearRect(0, 0, 1, 1);
      canvas.fillRect(0, 0, 1, 1);
      const d = canvas.getImageData(0, 0, 1, 1).data;
      return [d[0] ?? 0, d[1] ?? 0, d[2] ?? 0, (d[3] ?? 0) / 255];
    };
    const where = (el: Element) => {
      const name = (e: Element) => e.tagName.toLowerCase() + (e.id ? `#${e.id}` : [...e.classList].slice(0, 2).map((c) => `.${c}`).join(''));
      const parent = up(el);
      return el.id || el.classList.length || !parent ? name(el) : `${name(parent)} > ${name(el)}`;
    };
    const view: Edges = { l: 0, t: 0, r: innerWidth, b: innerHeight };
    const range = document.createRange();
    const modal = document.querySelector(':modal');
    const target = task?.scrollTo ? Number(task.scrollTo.split(':')[0]) : null;

    let signature = `${found.length}`;
    const runs: TextRun[] = [];
    for (const [id, node] of found.entries()) {
      const field = node instanceof Text ? null : node;
      const el = field ?? (node as Text).parentElement;
      if (!el) continue;
      // what a field shows: a select its option, a button its value, a text field what's typed or its placeholder. What's
      // typed is never quoted (it may be a secret): the report names it instead
      const select = field instanceof HTMLSelectElement ? field : null;
      const button = field instanceof HTMLInputElement && /^(submit|button|reset)$/.test(field.type) ? field : null;
      const typed = field && !(field instanceof HTMLSelectElement) && !button ? field : null;
      const shown = select ? select.selectedOptions[0]?.text ?? '' : button ? button.value || ({ submit: 'Submit', reset: 'Reset' } as Record<string, string>)[button.type] || '' : '';
      const placeholder = !!typed && !typed.value;
      const text = !field ? (node as Text).data : typed ? (placeholder ? `placeholder: ${typed.placeholder}` : 'what’s typed in the field') : shown;
      signature += `\n${text}`;
      if (target !== null && id !== target) continue;
      if (task?.unfade && !task.unfade.includes(id)) continue;
      if (!field && el.closest('script, style, noscript, template, title, textarea, select, option')) continue;
      if (field && !/\S/.test(typed ? typed.value || typed.placeholder : shown)) continue;
      if (!el.checkVisibility({ opacityProperty: true, visibilityProperty: true })) continue;
      if (el.closest(':disabled, [aria-disabled="true"], [inert]') || (modal && !modal.contains(el))) continue;
      if (task?.unfade) {
        const was = (w.a11yUnfaded ??= []);
        for (let e: Element | null = el; e; e = up(e)) {
          if ((e instanceof HTMLElement || e instanceof SVGElement) && Number(css(e).opacity) < 1 && !was.some(([x]) => x === e)) {
            was.push([e, e.style.getPropertyValue('opacity'), e.style.getPropertyPriority('opacity')]);
            e.style.setProperty('opacity', '1', 'important');
          }
        }
        continue;
      }
      const s = placeholder ? getComputedStyle(el, '::placeholder') : css(el);
      const paint = el instanceof SVGElement ? s.fill : s.webkitTextFillColor || s.color; // SVG's words are painted with fill
      if (paint === 'none') continue;
      const ink = rgba(paint);
      const fade = fadeOf(el);
      // what paints these words otherwise than in their ink, over the pixels below: not measured
      const letter = field ? null : getComputedStyle(el, '::first-letter');
      const unreadable = !ink ? `painted with “${paint}”, not one colour`
        : /text/.test(s.backgroundClip + s.getPropertyValue('-webkit-background-clip')) ? 'painted by its background (background-clip: text)'
          : letter && (letter.color !== s.color || letter.webkitTextFillColor !== s.webkitTextFillColor) ? 'its first letter has a colour of its own (::first-letter)'
            : effect(el);
      if (!unreadable && ink && ink[3] * fade === 0) continue; // nothing painted

      // each box (a field's: its content box, as a whole), and how to find it again once its element has scrolled
      const rects: { k: string; at: () => Edges }[] = [];
      if (field) {
        rects.push({ k: '0', at: () => {
          const cs = css(field);
          const b = inner(field);
          return { l: b.l + parseFloat(cs.paddingLeft), t: b.t + parseFloat(cs.paddingTop), r: b.r - parseFloat(cs.paddingRight), b: b.b - parseFloat(cs.paddingBottom) };
        } });
      } else {
        for (const m of (node as Text).data.matchAll(/\S+/gu)) {
          range.setStart(node, m.index);
          range.setEnd(node, m.index + m[0].length);
          const lines = range.getClientRects().length;
          for (let line = 0; line < lines; line++) {
            rects.push({ k: `${m.index}.${line}`, at: () => {
              range.setStart(node, m.index);
              range.setEnd(node, m.index + m[0].length);
              const r = range.getClientRects()[line];
              return r ? { l: r.left, t: r.top, r: r.right, b: r.bottom } : { l: 0, t: 0, r: 0, b: 0 };
            } });
          }
        }
      }
      const { hard, scrollers } = clips(el);
      const root = el.getRootNode() as Document | ShadowRoot;
      const boxes: Box[] = [];
      for (const { k, at } of rects) {
        const key = `${id}:${k}`;
        if (task?.scrollTo && key !== task.scrollTo) continue;
        let b = at();
        for (const c of hard) b = cut(b, c);
        if (b.r - b.l < 2 || b.b - b.t < 2) continue;
        if (task?.scrollTo) {
          const was = (w.a11yScrolled ??= []);
          for (let e = up(el); e; e = up(e)) {
            if (e.scrollHeight > e.clientHeight || e.scrollWidth > e.clientWidth) was.push([e, e.scrollLeft, e.scrollTop]);
          }
          const doc = document.scrollingElement;
          if (doc) was.push([doc, doc.scrollLeft, doc.scrollTop]);
          el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' });
          // then the word itself to the middle: of each scroller, innermost first, then of the viewport (a paragraph
          // taller than either has its middle elsewhere)
          for (const sc of [...scrollers].reverse()) {
            const [r, c] = [at(), inner(sc)];
            sc.scrollBy({ left: (r.l + r.r - c.l - c.r) / 2, top: (r.t + r.b - c.t - c.b) / 2, behavior: 'instant' });
          }
          const r = at();
          doc?.scrollBy({ left: (r.l + r.r - innerWidth) / 2, top: (r.t + r.b - innerHeight) / 2, behavior: 'instant' });
          continue;
        }
        let shown = cut(b, view);
        for (const c of scrollers) shown = cut(shown, inner(c));
        const whole = shown.l === b.l && shown.t === b.t && shown.r === b.r && shown.b === b.b;
        const covered = whole ? coveredBy(el, root, (b.l + b.r) / 2, (b.t + b.b) / 2) : null;
        boxes.push({ key, x: b.l, y: b.t, w: b.r - b.l, h: b.b - b.t, inView: whole && !covered, covered });
      }
      if (boxes.length) {
        runs.push({ id, where: where(el), text: text.replace(/\s+/g, ' ').trim(), size: parseFloat(s.fontSize), weight: parseInt(s.fontWeight, 10),
          ink: ink ?? [0, 0, 0, 1], fade, ...(unreadable ? { unreadable } : {}), boxes });
      }
    }
    if (task) return { runs: [], signature };

    // words drawn by CSS content (::before, ::after): no Range reaches them, so they are not measured
    for (const [i, e] of elements.entries()) {
      if (e.closest('script, style, noscript, template') || !e.checkVisibility({ opacityProperty: true, visibilityProperty: true })) continue;
      if (e.closest(':disabled, [aria-disabled="true"], [inert]') || (modal && !modal.contains(e))) continue;
      for (const pseudo of ['::before', '::after'] as const) {
        const ps = getComputedStyle(e, pseudo);
        if (ps.display === 'none' || ps.content === 'none' || ps.content === 'normal') continue;
        const words = [...ps.content.matchAll(/"((?:[^"\\]|\\.)*)"/g)].map((m) => m[1]).join('');
        if (!/\S/.test(words) && !/counter|attr\(|quote/.test(ps.content)) continue;
        let b = cut(inner(e), { l: -Infinity, t: -Infinity, r: Infinity, b: Infinity });
        for (const c of clips(e).hard) b = cut(b, c);
        if (b.r - b.l < 2 || b.b - b.t < 2) continue;
        runs.push({ id: -1 - (2 * i + (pseudo === '::after' ? 1 : 0)), where: `${where(e)}${pseudo}`, text: words || ps.content,
          size: parseFloat(ps.fontSize), weight: parseInt(ps.fontWeight, 10), ink: [0, 0, 0, 1], fade: 1, unreadable: 'drawn by CSS content',
          boxes: [{ key: `content:${i}${pseudo}`, x: b.l, y: b.t, w: b.r - b.l, h: b.b - b.t, inView: false, covered: null }] });
      }
    }
    return { runs, signature };
  }
}

/** In the page: puts back every opacity that textRuns' `unfade` changed. */
export function refade(): void {
  const w = window as { a11yUnfaded?: [HTMLElement | SVGElement, string, string][] };
  for (const [e, value, priority] of (w.a11yUnfaded ?? []).reverse()) {
    if (value) e.style.setProperty('opacity', value, priority);
    else e.style.removeProperty('opacity');
  }
  delete w.a11yUnfaded;
}

/**
 * In the page: hides every word (`on`), or shows them again, without moving anything. Only the glyphs go:
 * -webkit-text-fill-color, not `color`, so an icon, border or fill drawn in currentColor stays as it is. Transitions
 * stop, so the words go at once. The sheet is a constructed one (the pages' CSP refuses inline style), adopted by the
 * document and every open shadow root, however deep.
 */
export function hideWords(on: boolean): void {
  const w = window as { a11yHidden?: CSSStyleSheet };
  const roots: (Document | ShadowRoot)[] = [document];
  const walk = (root: Document | ShadowRoot) => {
    for (const e of root.querySelectorAll('*')) {
      if (e.shadowRoot) {
        roots.push(e.shadowRoot);
        walk(e.shadowRoot);
      }
    }
  };
  walk(document);
  if (on) {
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(`*, *::before, *::after, *::placeholder { -webkit-text-fill-color: transparent !important;
      -webkit-text-stroke-color: transparent !important; text-shadow: none !important; text-decoration-color: transparent !important;
      transition: none !important; } *::marker { color: transparent !important; }
      svg text, svg tspan, svg textPath { fill: transparent !important; stroke: transparent !important; }`);
    for (const r of roots) r.adoptedStyleSheets = [...r.adoptedStyleSheets, sheet];
    w.a11yHidden = sheet;
  } else {
    for (const r of roots) r.adoptedStyleSheets = r.adoptedStyleSheets.filter((s) => s !== w.a11yHidden);
    delete w.a11yHidden;
  }
}

/** A screenshot's pixels: RGBA, row by row, at the device's pixel ratio. */
export type Shot = { width: number; height: number; rgba: Uint8Array };

/** Reads the PNG that Playwright's screenshots are: 8 bits per channel, RGB or RGBA, not interlaced. */
export function decodePng(png: Buffer): Shot {
  if (png.readUInt32BE(0) !== 0x89504e47) throw new Error('not a PNG');
  let width = 0;
  let height = 0;
  let depth = 0;
  let type = 0;
  let interlace = 0;
  const data: Buffer[] = [];
  for (let at = 8; at < png.length;) {
    const length = png.readUInt32BE(at);
    const kind = png.toString('latin1', at + 4, at + 8);
    const chunk = png.subarray(at + 8, at + 8 + length);
    if (kind === 'IHDR') [width, height, depth, type, interlace] = [chunk.readUInt32BE(0), chunk.readUInt32BE(4), chunk.readUInt8(8), chunk.readUInt8(9), chunk.readUInt8(12)];
    if (kind === 'IDAT') data.push(chunk);
    if (kind === 'IEND') break;
    at += 12 + length;
  }
  if (depth !== 8 || (type !== 2 && type !== 6) || interlace) throw new Error(`a PNG this reader doesn't know (depth ${depth}, type ${type}, interlace ${interlace})`);
  const channels = type === 6 ? 4 : 3;
  const stride = width * channels;
  const raw = inflateSync(Buffer.concat(data));
  const of = (bytes: Uint8Array, i: number) => bytes[i] ?? 0; // every index here is in range
  const rgba = new Uint8Array(width * height * 4);
  let prev = new Uint8Array(stride);
  let line = new Uint8Array(stride);
  for (let y = 0; y < height; y++) {
    const filter = of(raw, y * (stride + 1));
    const row = raw.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1));
    for (let i = 0; i < stride; i++) {
      const a = i >= channels ? of(line, i - channels) : 0;
      const b = of(prev, i);
      const c = i >= channels ? of(prev, i - channels) : 0;
      let v = of(row, i);
      if (filter === 1) v += a;
      else if (filter === 2) v += b;
      else if (filter === 3) v += (a + b) >> 1;
      else if (filter === 4) {
        const p = a + b - c;
        const [pa, pb, pc] = [Math.abs(p - a), Math.abs(p - b), Math.abs(p - c)];
        v += pa <= pb && pa <= pc ? a : pb <= pc ? b : c;
      }
      line[i] = v;
    }
    for (let x = 0; x < width; x++) {
      const o = (y * width + x) * 4;
      rgba[o] = of(line, x * channels);
      rgba[o + 1] = of(line, x * channels + 1);
      rgba[o + 2] = of(line, x * channels + 2);
      rgba[o + 3] = channels === 4 ? of(line, x * channels + 3) : 255;
    }
    [prev, line] = [line, prev];
  }
  return { width, height, rgba };
}

const linear = Float64Array.from({ length: 256 }, (_, i) => {
  const c = i / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
});
const luminance = (r: number, g: number, b: number) => 0.2126 * (linear[r] ?? 0) + 0.7152 * (linear[g] ?? 0) + 0.0722 * (linear[b] ?? 0);

/** WCAG's contrast ratio between two luminances. */
const ratio = (a: number, b: number) => (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);

/**
 * A box's contrast: the ink alpha-composited over each background pixel in the box as the browser paints it (in sRGB),
 * against that pixel; the box's contrast is its worst patch's (PATCH). A word in a faded group (an ancestor's opacity) is
 * painted inside the group first, over what the group itself paints there (`unfaded`, the same area shot with the group
 * opaque), and the group then fades over what is below it: on screen, the background plus fade × alpha × (ink − the
 * group's own pixel). Without `unfaded`, the group paints nothing of its own there. `dpr` turns CSS pixels into the
 * shot's.
 */
export function boxContrast(shot: Shot, box: Box, ink: TextRun['ink'], fade: number, dpr: number, unfaded?: Shot): number {
  const [x0, y0] = [Math.max(0, Math.round(box.x * dpr)), Math.max(0, Math.round(box.y * dpr))];
  const [x1, y1] = [Math.min(shot.width, Math.round((box.x + box.w) * dpr)), Math.min(shot.height, Math.round((box.y + box.h) * dpr))];
  const [r, g, b] = ink;
  const a = ink[3] * fade;
  const own = unfaded ?? shot;
  const over = (t: number, u: number, v: number) => Math.min(255, Math.max(0, Math.round(u + a * (t - v))));
  const [width, height] = [x1 - x0, y1 - y0];
  if (width <= 0 || height <= 0) return NaN;
  const contrast = new Float64Array(width * height);
  for (let y = y0; y < y1; y++) {
    for (let x = x0; x < x1; x++) {
      const o = (y * shot.width + x) * 4;
      const [br, bg, bb] = [shot.rgba[o] ?? 0, shot.rgba[o + 1] ?? 0, shot.rgba[o + 2] ?? 0];
      const [gr, gg, gb] = [own.rgba[o] ?? 0, own.rgba[o + 1] ?? 0, own.rgba[o + 2] ?? 0];
      contrast[(y - y0) * width + (x - x0)] = ratio(luminance(over(r, br, gr), over(g, bg, gg), over(b, bb, gb)), luminance(br, bg, bb));
    }
  }
  return worstPatch(contrast, width, height);
}

/**
 * The worst patch of a grid of contrasts that is more than PATCH of its width wide and of its height high: the lowest,
 * over every such patch, of the best contrast in it (a sliding maximum along the rows, then down the columns).
 */
export function worstPatch(contrast: Float64Array, width: number, height: number): number {
  const [kw, kh] = [Math.min(width, Math.floor(PATCH * width) + 1), Math.min(height, Math.floor(PATCH * height) + 1)];
  const across = new Float64Array((width - kw + 1) * height);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x + kw <= width; x++) {
      let best = 0;
      for (let i = 0; i < kw; i++) best = Math.max(best, contrast[y * width + x + i] ?? 0);
      across[y * (width - kw + 1) + x] = best;
    }
  }
  let worst = Infinity;
  for (let x = 0; x + kw <= width; x++) {
    for (let y = 0; y + kh <= height; y++) {
      let best = 0;
      for (let i = 0; i < kh; i++) best = Math.max(best, across[(y + i) * (width - kw + 1) + x] ?? 0);
      worst = Math.min(worst, best);
    }
  }
  return worst;
}
