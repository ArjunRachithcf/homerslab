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
 * timeout), the group (its runner and workers) is killed and the run ends with what it said so far. The browsers run in
 * process groups of their own, out of the kill's reach: each exits when its pipe to the killed worker closes.
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

/**
 * axe's report: the rule's paragraph, and in that paragraph the element. The paragraph ends at the next rule's line
 * (`<id> (…`) or at the next error (`Error:`), whose first rule follows `Received: "` mid-line.
 */
const finds = (rule: string, element: string) =>
  new RegExp(`${rule}(?:(?!\\n\\s*[a-z][a-z0-9-]* \\(|\\nError:)[^])*\\n\\s*- ${element}\\n`);
/** The pixel check's report: a line for the element, with the words, the contrast found and the contrast needed. */
const findsWord = (element: string, needs: string) => new RegExp(`\\n\\s*- ${element} “[^”\\n]+”: \\d+\\.\\d\\d:1, needs ${needs} `);
type Expected = { check: string; says: RegExp; axePasses?: boolean };

test('a rule’s paragraph ends with its error', () => {
  const two = ['Error: axe (WCAG 2.2 AA) on “one”', '', 'Expected: ""', 'Received: "label (critical): Form elements must have labels',
    '  https://example.org/label', '"', 'Error: axe (WCAG 2.2 AA) on “two”', '', 'Expected: ""',
    'Received: "color-contrast (serious): Elements must meet minimum color contrast ratio thresholds', '  https://example.org/cc',
    '  - #planted-unlabelled', '"'].join('\n');
  expect(two).not.toMatch(finds('label \\(critical\\)', '#planted-unlabelled'));
  expect(two).toMatch(finds('color-contrast \\(serious\\)', '#planted-unlabelled'));
});

test('accessible() fails the test, naming the screen and the rule', async () => {
  test.slow(); // a Playwright inside a Playwright
  const out = await mkdtemp(path.join(tmpdir(), 'homerslab-planted-'));
  try {
    // this worker's own Playwright variables stay out of the child's environment; two workers for twelve tests
    const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !/^(TEST_|PW_)/.test(k)));
    const child = await playwright(['test', 'e2e/planted.a11y.spec.ts', '--project', test.info().project.name,
      '--reporter', 'json', '--output', out, '--retries', '0', '--workers', '2'], { ...env, HOMERSLAB_PLANTED: '1' },
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

    // which check fails each plant (axe, or the pixels), and what its report must say
    const axe = (rule: string, element: string): Expected => ({ check: 'axe (WCAG 2.2 AA)', says: finds(rule, element) });
    const pixelCheck = 'contrast against the pixels (WCAG 2.2 AA)';
    const pixels = (element: string, needs: string): Expected => ({ check: pixelCheck, says: findsWord(element, needs), axePasses: true });
    const expected: [string, Expected][] = [
      ['planted: a field without a label, light', axe('label \\(critical\\)', '#planted-unlabelled')],
      ['planted: a field without a label, dark', axe('label \\(critical\\)', '#planted-unlabelled')],
      ['planted: a faint lead on a gradient that looks solid', axe('color-contrast \\(not decided by axe, with every material solid\\)', '\\.as-lead')],
      ['planted: a faint lead the viewport’s edge cuts', axe('color-contrast \\(serious\\)', '\\.as-lead')],
      ['planted: a cut lead the second run can’t see', axe('color-contrast \\(not decided by axe, with every material solid\\)', '\\.as-lead')],
      ['planted: a faint lead on glass', pixels('p\\.as-lead', '4\\.5:1')],
      ['planted: a faint footer on the wallpaper', pixels('footer\\.as-foot', '4\\.5:1')],
      ['planted: a faded lead on glass', pixels('p\\.as-lead', '4\\.5:1')],
      ['planted: a faint label floating over a field', { check: pixelCheck, says: findsWord('span#planted-float', '4\\.5:1') }],
      ['planted: a faint toast in a box that hides its overflow', { check: pixelCheck, says: findsWord('p#planted-toast', '4\\.5:1') }],
      ['planted: dark words in a faded grey group', { check: pixelCheck, says: findsWord('p#planted-dim', '4\\.5:1') }],
      ['planted: a word half off the screen', { check: pixelCheck,
        says: /\n\s*- p#planted-offscreen “Half off the screen”: not measured, \d+ words?: never wholly in view/ }],
    ];
    expect([...seen.keys()].sort()).toEqual(expected.map(([t]) => t).sort());
    for (const [title, { check, says, axePasses }] of expected) {
      expect(seen.get(title)?.status, title).toBe('failed');
      expect(seen.get(title)?.errors, title).toContain(`${check} on “${title}”`);
      expect(seen.get(title)?.errors, title).toMatch(says);
      // a plant only the pixels catch: axe passed it, so it shows what axe can't measure
      if (axePasses) expect(seen.get(title)?.errors, title).not.toContain('axe (WCAG 2.2 AA) on');
    }
  } finally {
    await rm(out, { recursive: true, force: true });
  }
});
