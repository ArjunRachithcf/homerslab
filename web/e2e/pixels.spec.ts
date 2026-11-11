// pixels.ts's sums, without a browser: which patch of a word's box decides its contrast.
import { expect, test } from '@playwright/test';
import { worstPatch } from './pixels';

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
