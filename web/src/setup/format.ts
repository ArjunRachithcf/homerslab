import { t } from '../i18n/t';

/** Installed memory as people know it: the kernel's total, a little under what's fitted, rounded up to whole GB. */
export const memory = (bytes: number): string => t('unit.gb', { n: Math.ceil(bytes / 2 ** 30) });

/** A disk's size as its maker sells it, in decimal units. */
export function diskSize(bytes: number): string {
  const one = (v: number) => String(Math.round(v * 10) / 10);
  if (bytes >= 1e12) return t('unit.tb', { n: one(bytes / 1e12) });
  if (bytes >= 1e9) return t('unit.gb', { n: one(bytes / 1e9) });
  return t('unit.mb', { n: one(bytes / 1e6) });
}
