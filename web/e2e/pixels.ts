// Contrast against the pixels: what a word's contrast is on the screen itself, glass, blur and wallpaper included,
// where axe can only read colours from the style sheets. a11y.ts's accessible() runs it on every screen; this file has
// its parts: what runs in the page (finding each word's boxes, hiding the words), reading a screenshot, and the sums.
import { inflateSync } from 'node:zlib';

/**
 * Which of a box's background pixels decides its contrast: the 5th percentile of the contrasts its pixels give. The
 * worst pixel alone would fail a word that reads well over a 1px line the box happens to cross (the glass's rim, a
 * field's border, a focus ring), while a word on a background that is too light under a twentieth of its box, about a
 * stroke's width under every letter, fails.
 */
export const PERCENTILE = 0.05;

/** WCAG 2.2's AA minimum (1.4.3): 4.5:1, or 3:1 for large text (24px and up, or 18.66px and up in bold). */
export const needed = (size: number, weight: number): number => (size >= 24 || (size >= 18.66 && weight >= 700) ? 3 : 4.5);

/** A word's box on the screen, in CSS pixels from the viewport's corner; `inView` once nothing hides any of it. */
export type Box = { key: string; x: number; y: number; w: number; h: number; inView: boolean; covered: boolean };

/** A run of text: a text node (or a field's value or placeholder), with its ink (sRGB 0-255, alpha 0-1) and boxes. */
export type TextRun = { id: number; where: string; text: string; size: number; weight: number; ink: [number, number, number, number]; boxes: Box[] };

/**
 * In the page: every run of text it shows, with a box per word (per line, when a word breaks across two). Text that
 * isn't shown (hidden, transparent, clipped away, collapsed) has no boxes, and nor does text in an inactive control,
 * which WCAG exempts. With `scrollTo` (a box's key), it instead scrolls that box to the middle of the viewport, noting
 * every scroll position it changes in `a11yScrolled` for a11y.ts to put back, and returns nothing.
 */
export function textRuns(scrollTo: string | null): TextRun[] {
  // the inks as the page paints them: hideWords' sheet off while they are read (in one task, so nothing is painted)
  const hidden = (window as { a11yHidden?: CSSStyleSheet }).a11yHidden;
  if (hidden) hidden.disabled = true;
  try {
    return collect();
  } finally {
    if (hidden) hidden.disabled = false;
  }

  function collect(): TextRun[] {
    const up = (e: Element): Element | null => e.parentElement ?? ((e.parentNode as ShadowRoot | null)?.host ?? null);
    type Edges = { l: number; t: number; r: number; b: number };
    const cut = (a: Edges, b: Edges): Edges => ({ l: Math.max(a.l, b.l), t: Math.max(a.t, b.t), r: Math.min(a.r, b.r), b: Math.min(a.b, b.b) });
    const inner = (e: Element): Edges => {
      const r = e.getBoundingClientRect();
      return { l: r.left + e.clientLeft, t: r.top + e.clientTop, r: r.left + e.clientLeft + e.clientWidth, b: r.top + e.clientTop + e.clientHeight };
    };

    // the text, in document order (open shadow roots included), and fields whose words are their value or placeholder
    const found: (Text | HTMLInputElement | HTMLTextAreaElement)[] = [];
    const walk = (root: Node) => {
      const w = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT);
      for (let n = w.nextNode(); n; n = w.nextNode()) {
        if (n instanceof Text) {
          if (/\S/.test(n.data)) found.push(n);
        } else if (n instanceof HTMLTextAreaElement || (n instanceof HTMLInputElement && /^(text|search|email|url|tel|number|password)$/.test(n.type))) {
          found.push(n);
        } else if (n instanceof Element && n.shadowRoot) {
          walk(n.shadowRoot);
        }
      }
    };
    walk(document.body);

    // the boxes that clip an element's words: overflow that hides for good, and scrollers (shown once scrolled to)
    type Clips = { hard: Edges[]; scrollers: Element[] };
    const clipCache = new Map<Element, Clips>();
    const clips = (el: Element): Clips => {
      let c = clipCache.get(el);
      if (c) return c;
      const parent = up(el);
      const above: Clips = parent ? clips(parent) : { hard: [], scrollers: [] };
      const s = getComputedStyle(el);
      c = above;
      if (s.display !== 'inline' && s.display !== 'contents' && (s.overflowX !== 'visible' || s.overflowY !== 'visible')) {
        const scrolls = /auto|scroll/.test(s.overflowX + s.overflowY);
        c = scrolls ? { hard: above.hard, scrollers: [...above.scrollers, el] } : { hard: [...above.hard, inner(el)], scrollers: above.scrollers };
      }
      clipCache.set(el, c);
      return c;
    };

    const canvas = new OffscreenCanvas(1, 1).getContext('2d', { willReadFrequently: true });
    const rgba = (colour: string): [number, number, number, number] => {
      const legacy = /^rgba?\(([\d.]+), ([\d.]+), ([\d.]+)(?:, ([\d.]+))?\)$/.exec(colour);
      if (legacy) return [Number(legacy[1]), Number(legacy[2]), Number(legacy[3]), legacy[4] === undefined ? 1 : Number(legacy[4])];
      const srgb = /^color\(srgb ([\d.e-]+) ([\d.e-]+) ([\d.e-]+)(?: \/ ([\d.]+))?\)$/.exec(colour);
      if (srgb) return [Number(srgb[1]) * 255, Number(srgb[2]) * 255, Number(srgb[3]) * 255, srgb[4] === undefined ? 1 : Number(srgb[4])];
      // any other colour space: as the browser paints it (a canvas ignores a colour it can't read: the sentinel stays)
      const sentinel = '#010203';
      if (canvas) canvas.fillStyle = sentinel;
      if (canvas) canvas.fillStyle = colour;
      if (!canvas || (canvas.fillStyle === sentinel && colour !== sentinel)) throw new Error(`can't read the colour “${colour}”`);
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

    const runs: TextRun[] = [];
    found.forEach((node, id) => {
      const field = node instanceof Text ? null : node;
      const el = field ?? (node as Text).parentElement;
      if (!el) return;
      if (!field && el.closest('script, style, noscript, template, title, textarea, select, option')) return;
      if (!el.checkVisibility({ opacityProperty: true, visibilityProperty: true })) return;
      if (el.closest(':disabled, [aria-disabled="true"]')) return;
      const placeholder = !!field && !field.value;
      if (field ? !/\S/.test(field.value || field.placeholder) : false) return;
      // a field's value is never quoted (it may be a secret someone typed): the report names it instead
      const text = field ? (placeholder ? `placeholder: ${field.placeholder}` : 'what’s typed in the field') : (node as Text).data;
      const s = getComputedStyle(el, placeholder ? '::placeholder' : null);
      const ink = rgba(el instanceof SVGElement ? s.fill : s.webkitTextFillColor || s.color); // SVG's words are painted with fill
      for (let e: Element | null = el; e; e = up(e)) ink[3] *= +getComputedStyle(e).opacity;
      if (ink[3] === 0) return; // nothing painted (words in a gradient through background-clip: text, too)

      // a box per word (a field: its content box, as a whole), clipped; in view once the viewport and every scroller show
      // all of it and nothing covers its middle
      const { hard, scrollers } = clips(el);
      // each box, and how to find it again once its element has scrolled
      const rects: { k: string; at: () => Edges }[] = [];
      if (field) {
        rects.push({ k: '0', at: () => {
          const cs = getComputedStyle(field);
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
      const boxes: Box[] = [];
      for (const { k, at } of rects) {
        let b = at();
        for (const c of hard) b = cut(b, c);
        if (b.r - b.l < 2 || b.b - b.t < 2) continue;
        let shown = cut(b, view);
        for (const c of scrollers) shown = cut(shown, inner(c));
        const whole = shown.l === b.l && shown.t === b.t && shown.r === b.r && shown.b === b.b;
        const root = el.getRootNode() as Document | ShadowRoot;
        const hit = whole ? root.elementFromPoint((b.l + b.r) / 2, (b.t + b.b) / 2) : null;
        const covered = whole && !(hit && (hit === el || el.contains(hit) || hit.contains(el)));
        const key = `${id}:${k}`;
        if (key === scrollTo) {
          const w = window as { a11yScrolled?: [Element, number, number][] };
          const was = (w.a11yScrolled ??= []);
          for (let e = up(el); e; e = up(e)) {
            if (e.scrollHeight > e.clientHeight || e.scrollWidth > e.clientWidth) was.push([e, e.scrollLeft, e.scrollTop]);
          }
          const doc = document.scrollingElement;
          if (doc) was.push([doc, doc.scrollLeft, doc.scrollTop]);
          el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' });
          // a word in a paragraph taller than the viewport: the paragraph's middle isn't the word's
          const now = at();
          if (now.t < 0 || now.b > innerHeight) doc?.scrollBy({ top: (now.t + now.b) / 2 - innerHeight / 2, behavior: 'instant' });
          return;
        }
        boxes.push({ key, x: b.l, y: b.t, w: b.r - b.l, h: b.b - b.t, inView: whole && !covered, covered });
      }
      if (boxes.length) {
        runs.push({ id, where: where(el), text: text.replace(/\s+/g, ' ').trim(), size: parseFloat(s.fontSize), weight: parseInt(s.fontWeight, 10), ink, boxes });
      }
    });
    if (scrollTo) return [];
    return runs;
  }
}

/**
 * In the page: hides every word (`on`), or shows them again, without moving anything. Only the glyphs go:
 * -webkit-text-fill-color, not `color`, so an icon, border or fill drawn in currentColor stays as it is. Transitions
 * stop, so the words go at once. The sheet is a constructed one: the pages' CSP refuses inline style.
 */
export function hideWords(on: boolean): void {
  const w = window as { a11yHidden?: CSSStyleSheet };
  const roots: (Document | ShadowRoot)[] = [document];
  for (const e of document.querySelectorAll('*')) if (e.shadowRoot) roots.push(e.shadowRoot);
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
 * A box's contrast: the ink, alpha-composited over each background pixel in the box as the browser paints it (in
 * sRGB), against that pixel; the box's contrast is the PERCENTILE of them. `dpr` turns CSS pixels into the shot's.
 */
export function boxContrast(shot: Shot, box: Box, ink: TextRun['ink'], dpr: number): number {
  const [x0, y0] = [Math.max(0, Math.round(box.x * dpr)), Math.max(0, Math.round(box.y * dpr))];
  const [x1, y1] = [Math.min(shot.width, Math.round((box.x + box.w) * dpr)), Math.min(shot.height, Math.round((box.y + box.h) * dpr))];
  const [r, g, b, a] = ink;
  const found: number[] = [];
  for (let y = y0; y < y1; y++) {
    for (let x = x0; x < x1; x++) {
      const o = (y * shot.width + x) * 4;
      const [br, bg, bb] = [shot.rgba[o] ?? 0, shot.rgba[o + 1] ?? 0, shot.rgba[o + 2] ?? 0];
      const over = (t: number, u: number) => Math.round(t * a + u * (1 - a));
      found.push(ratio(luminance(over(r, br), over(g, bg), over(b, bb)), luminance(br, bg, bb)));
    }
  }
  if (!found.length) return NaN;
  found.sort((p, q) => p - q);
  return found[Math.floor(PERCENTILE * (found.length - 1))] ?? NaN;
}
