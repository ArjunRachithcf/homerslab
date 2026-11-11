// Every word the surfaces show comes from a translation file; English is the first. t() fills {name} placeholders
// with text; rich() fills them with elements, such as a host name in bold.
import { Fragment, type ReactNode } from 'react';
import en from './en.json';

export type Key = keyof typeof en;
const strings: Record<Key, string> = en;

export function t(key: Key, vars: Record<string, string | number> = {}): string {
  return strings[key].replace(/\{(\w+)\}/g, (m, name: string) => (name in vars ? String(vars[name]) : m));
}

export function rich(key: Key, vars: Record<string, ReactNode>): ReactNode {
  return strings[key].split(/(\{\w+\})/).map((part, i) => {
    const name = /^\{(\w+)\}$/.exec(part)?.[1];
    return <Fragment key={i}>{name !== undefined && name in vars ? vars[name] : part}</Fragment>;
  });
}
