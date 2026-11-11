// The check of the check: planted.a11y.spec.ts's tests, run in a Playwright of their own, must each fail, and their
// errors must name the screen and the rule axe found broken.
import { execFile } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { expect, test } from '@playwright/test';

const run = promisify(execFile);
const web = path.join(path.dirname(fileURLToPath(import.meta.url)), '..');

type Spec = { title: string; tests: { results: { status: string; errors: { message?: string }[] }[] }[] };
type Suite = { specs?: Spec[]; suites?: Suite[] };
const specs = (s: Suite): Spec[] => [...(s.specs ?? []), ...(s.suites ?? []).flatMap(specs)];
// eslint-disable-next-line no-control-regex -- the terminal's colour codes in Playwright's messages
const plain = (s: string) => s.replace(/\u001b\[[0-9;]*m/g, '');

test('accessible() fails the test, naming the screen and the rule', async () => {
  test.slow(); // a Playwright inside a Playwright
  const out = await mkdtemp(path.join(tmpdir(), 'homerslab-planted-'));
  try {
    // this worker's own Playwright variables stay out of the child's environment; one worker is enough for three tests
    const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !/^(TEST_|PW_)/.test(k)));
    const child = await run('npx', ['playwright', 'test', 'e2e/planted.a11y.spec.ts', '--project', test.info().project.name,
      '--reporter', 'json', '--output', out, '--retries', '0', '--workers', '1'], {
      cwd: web, env: { ...env, HOMERSLAB_PLANTED: '1' }, maxBuffer: 64 << 20,
    }).catch((e: { stdout?: string; stderr?: string; message: string }) => e); // the child fails, as it should
    let report: Suite & { suites: Suite[] };
    try {
      report = JSON.parse(child.stdout ?? '') as typeof report;
    } catch {
      throw new Error(`the planted run gave no report: ${'message' in child ? child.message : ''}\n${child.stderr ?? ''}`);
    }
    const seen = new Map(report.suites.flatMap(specs).map((s) => {
      const r = s.tests[0]?.results[0];
      return [s.title, { status: r?.status, errors: plain((r?.errors ?? []).map((e) => e.message ?? '').join('\n')) }];
    }));

    const expected: [string, RegExp][] = [
      ['planted: a field without a label, light', /label \(critical\)[^]*#planted-unlabelled/],
      ['planted: a field without a label, dark', /label \(critical\)[^]*#planted-unlabelled/],
      ['planted: a faint lead on a gradient that looks solid', /color-contrast \(not decided by axe, with every material solid\)[^]*\.as-lead/],
    ];
    expect([...seen.keys()].sort()).toEqual(expected.map(([t]) => t).sort());
    for (const [title, rule] of expected) {
      expect(seen.get(title)?.status, title).toBe('failed');
      expect(seen.get(title)?.errors, title).toContain(`axe (WCAG 2.2 AA) on “${title}”`);
      expect(seen.get(title)?.errors, title).toMatch(rule);
    }
  } finally {
    await rm(out, { recursive: true, force: true });
  }
});
