// pixels.ts's sums, without a browser: which patch of a word's box decides its contrast.
import { expect, test } from '@playwright/test';
import { glyphsShow, SENTINEL, type Shot, worstPatch } from './pixels';

/** A box `width` by `height` of contrasts: 1.5 where `bad(x, y)`, 12 elsewhere. */
const box = (width: number, height: number, bad: (x: number, y: number) => boolean) =>
  Float64Array.from({ length: width * height }, (_, i) => (bad(i % width, Math.floor(i / width)) ? 1.5 : 12));

test('worstPatch: what is too low under part of a word decides, a hairline doesn’t', () => {
  const [w, h] = [200, 20];
  expect(worstPatch(box(w, h, () => false), w, h)).toBe(12);
  expect(worstPatch(box(w, h, (x) => x >= w / 2), w, h), 'the right half').toBe(1.5);
  expect(worstPatch(box(w, h, (_, y) => y >= h / 2), w, h), 'the lower half').toBe(1.5);
  expect(worstPatch(box(w, h, (x) => x % 5 !== 0), w, h), 'light stripes, one column in five').toBe(1.5);
  expect(worstPatch(box(w, h, (x) => x % 8 !== 0), w, h), 'light stripes, one column in eight').toBe(1.5);
  expect(worstPatch(box(w, h, (x) => x >= 60 && x < 70), w, h), 'a band 10px wide').toBe(1.5);
  expect(worstPatch(box(w, h, (_, y) => y === 10), w, h), 'a 1px line across').toBe(12);
  expect(worstPatch(box(w, h, (x) => x === 100), w, h), 'a 1px line down').toBe(12);
});

type RGB = [number, number, number];
/** A shot 30 by 12 pixels, each `at(x, y)`. */
const shot = (at: (x: number, y: number) => RGB): Shot => {
  const [width, height] = [30, 12];
  const rgba = new Uint8Array(width * height * 4);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const [r, g, b] = at(x, y);
      rgba.set([Math.round(r), Math.round(g), Math.round(b), 255], (y * width + x) * 4);
    }
  }
  return { width, height, rgba };
};
const whole = { key: '0:0', x: 0, y: 0, w: 30, h: 12, inView: true, covered: null };
const glyph = (x: number) => x % 3 === 0; // a glyph's pixels: every third column
/** `a` mixed with `b`, `t` of the way to `b`. */
const mix = (a: RGB, b: RGB, t: number): RGB => [0, 1, 2].map((c) => (a[c] ?? 0) + t * ((b[c] ?? 0) - (a[c] ?? 0))) as RGB;
/** On `ground`: the glyphs hidden, and in the sentinel with `through` of its change showing on the screen. */
const marked = (ground: RGB, through: number): [Shot, Shot] =>
  [shot(() => ground), shot((x) => (glyph(x) ? mix(ground, SENTINEL, through) : ground))];

test('glyphsShow: glyphs show when the sentinel’s change comes through, all of it, faded as the word is', () => {
  const white: RGB = [255, 255, 255];
  expect(glyphsShow(...marked(white, 1), whole, 1, 1)).toBe(true);
  expect(glyphsShow(...marked(white, 0.95), whole, 1, 1), 'a glyph’s edge, antialiased').toBe(true);
  expect(glyphsShow(...marked(white, 0.35), whole, 0.35, 1), 'faded to 0.35').toBe(true);
  // in a faded group with a colour of its own, the glyph changes from the group's own pixel (the unfaded shot), not
  // from the screen's: a pink group at 0.5 over green
  const [green, pink]: [RGB, RGB] = [[0, 255, 0], [255, 128, 255]];
  const screen = mix(green, pink, 0.5);
  const inGroup = [shot(() => screen), shot((x) => (glyph(x) ? mix(green, SENTINEL, 0.5) : screen))] as const;
  expect(glyphsShow(...inGroup, whole, 0.5, 1, shot(() => pink)), 'in a faded pink group').toBe(true);
});

test('glyphsShow: glyphs under a cover that lets part of them through don’t show', () => {
  const white: RGB = [255, 255, 255];
  // black words under white at 60% (rgba, or opacity), or under #999 that screens: 40% of the sentinel's change
  expect(glyphsShow(...marked(white, 0.4), whole, 1, 1), 'under a cover at 60%').toBe(false);
  expect(glyphsShow(...marked(white, 0.85), whole, 1, 1), 'under a cover at 15%').toBe(false);
  expect(glyphsShow(...marked(white, 0.3), whole, 0.5, 1), 'faded to 0.5, under a cover at 40%').toBe(false);
  expect(glyphsShow(...marked(white, 0), whole, 1, 1), 'under an opaque cover').toBe(false);
  expect(glyphsShow(...marked(SENTINEL, 1), whole, 1, 1), 'on the sentinel’s colour').toBe(null);
});
