import { t } from '../i18n/t';

/** Memory in whole gigabytes (GiB), or megabytes below one. */
export const memory = (bytes: number): string =>
  bytes < 2 ** 30 ? t('unit.mb', { n: Math.round(bytes / 2 ** 20) }) : t('unit.gb', { n: Math.round(bytes / 2 ** 30) });

/** Disk space as disks are sold, in decimal units. */
export function disk(bytes: number): string {
  const one = (v: number) => String(Math.round(v * 10) / 10);
  if (bytes >= 1e12) return t('unit.tb', { n: one(bytes / 1e12) });
  if (bytes >= 1e9) return t('unit.gb', { n: one(bytes / 1e9) });
  return t('unit.mb', { n: one(bytes / 1e6) });
}

/** A list in words: "a, b and c". */
export function and(items: string[]): string {
  if (items.length < 2) return items.join('');
  return t('plan.and', { a: items.slice(0, -1).join(', '), b: items[items.length - 1] ?? '' });
}

/** A phrase that continues a sentence: its first letter lowercased, unless its first word is a name. */
export function lower(s: string): string {
  const first = s.split(' ')[0] ?? '';
  return /\p{Lu}/u.test(first.slice(1)) ? s : s.charAt(0).toLowerCase() + s.slice(1);
}
