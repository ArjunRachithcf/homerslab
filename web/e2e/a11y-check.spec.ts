// The check of the check: planted.a11y.spec.ts's tests, run in a Playwright of their own, must each fail, and their
// errors must name the screen and the rule axe found broken.
import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test } from '@playwright/test';

const web = path.join(path.dirname(fileURLToPath(import.meta.url)), '..');

/**
 * Runs Playwright on its own, in a process group of its own: if it hasn't finished within `ms` (less than the test's
 * timeout), the whole group (its runner, workers and browsers) is killed and the run ends with what it said so far.
 */
function playwright(args: string[], env: NodeJS.ProcessEnv, ms: number): Promise<{ stdout: string; stderr: string; ended: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [path.join(web, 'node_modules', '@playwright', 'test', 'cli.js'), ...args],
      { cwd: web, env, detached: true, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (b: Buffer) => { stdout += b; });
    child.stderr.on('data', (b: Buffer) => { stderr += b; });
    const timer = setTimeout(() => {
      if (child.pid) process.kill(-child.pid, 'SIGKILL');
    }, ms);
    child.on('error', (e) => { clearTimeout(timer); reject(e); });
    child.on('close', (code, signal) => {
      clearTimeout(timer);
      resolve({ stdout, stderr, ended: signal ? `killed by ${signal} after ${ms} ms` : `exit ${code}` });
    });
  });
}

type Spec = { title: string; tests: { results: { status: string; errors: { message?: string }[] }[] }[] };
type Suite = { specs?: Spec[]; suites?: Suite[] };
const specs = (s: Suite): Spec[] => [...(s.specs ?? []), ...(s.suites ?? []).flatMap(specs)];
// eslint-disable-next-line no-control-regex -- the terminal's colour codes in Playwright's messages
const plain = (s: string) => s.replace(/\u001b\[[0-9;]*m/g, '');

test('accessible() fails the test, naming the screen and the rule', async () => {
  test.slow(); // a Playwright inside a Playwright
  const out = await mkdtemp(path.join(tmpdir(), 'homerslab-planted-'));
  try {
    // this worker's own Playwright variables stay out of the child's environment; one worker is enough for four tests
    const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !/^(TEST_|PW_)/.test(k)));
    const child = await playwright(['test', 'e2e/planted.a11y.spec.ts', '--project', test.info().project.name,
      '--reporter', 'json', '--output', out, '--retries', '0', '--workers', '1'], { ...env, HOMERSLAB_PLANTED: '1' },
    test.info().timeout - 15_000); // it fails, as it should
    let report: Suite & { suites: Suite[] };
    try {
      report = JSON.parse(child.stdout) as typeof report;
    } catch {
      throw new Error(`the planted run gave no report (${child.ended}):\n${child.stderr}`);
    }
    const seen = new Map(report.suites.flatMap(specs).map((s) => {
      const r = s.tests[0]?.results[0];
      return [s.title, { status: r?.status, errors: plain((r?.errors ?? []).map((e) => e.message ?? '').join('\n')) }];
    }));

    // the rule's paragraph in the report, and in that paragraph (before the next rule's line, `<id> (…`) the element
    const finds = (rule: string, element: string) => new RegExp(`${rule}(?:(?!\\n\\s*[a-z][a-z0-9-]* \\()[^])*\\n\\s*- ${element}\\n`);
    const expected: [string, RegExp][] = [
      ['planted: a field without a label, light', finds('label \\(critical\\)', '#planted-unlabelled')],
      ['planted: a field without a label, dark', finds('label \\(critical\\)', '#planted-unlabelled')],
      ['planted: a faint lead on a gradient that looks solid', finds('color-contrast \\(not decided by axe, with every material solid\\)', '\\.as-lead')],
      ['planted: a faint lead the viewport’s edge cuts', finds('color-contrast \\(serious\\)', '\\.as-lead')],
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
