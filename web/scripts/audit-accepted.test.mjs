// The accepted advisories (#115): the npm audit job warns only for an advisory that isn't in web/audit-accepted.json.
// Run with `npm run test:unit` (node:test, nothing to install).
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

import { check } from './audit-accepted.mjs';
import { commands } from './runner-commands.mjs';

const MINIMATCH = 'GHSA-g3hv-75wg-m249';

// A report in npm audit --json's shape (auditReportVersion 2): one entry per affected package; `via` holds advisories
// (objects) and the names of the packages they arrive through (strings).
const advisory = (ghsa, name, title, severity = 'moderate') => ({
  source: 1000,
  name,
  dependency: name,
  title,
  url: `https://github.com/advisories/${ghsa}`,
  severity,
  range: '<1.0.0',
});
const report = (...entries) => {
  const vulnerabilities = {};
  for (const [name, via] of entries) vulnerabilities[name] = { name, severity: 'moderate', isDirect: false, via, effects: [] };
  return JSON.stringify({ auditReportVersion: 2, vulnerabilities, metadata: {} });
};
const accepted = (...ids) => JSON.stringify({ advisories: Object.fromEntries(ids.map((id) => [id, 'known, development only'])) });
const minimatch = ['minimatch', [advisory(MINIMATCH, 'minimatch', 'minimatch has potential denial of service')]];
const eslint = ['eslint', ['minimatch']];

const warnings = (out) => out.filter((l) => l.startsWith('::warning'));
const notices = (out) => out.filter((l) => l.startsWith('::notice'));

test('an accepted advisory is listed, with no warning', () => {
  const out = check(report(minimatch, eslint), accepted(MINIMATCH));
  assert.deepEqual(warnings(out), []);
  assert.deepEqual(notices(out), []);
  assert.ok(out.some((l) => l.includes(MINIMATCH) && l.includes('minimatch')), out.join('\n'));
});

test('an advisory not accepted warns, naming it and its package', () => {
  const lodash = ['lodash', [advisory('GHSA-35jh-r3h4-6jhm', 'lodash', 'Command Injection in lodash', 'high')]];
  const out = check(report(minimatch, eslint, lodash), accepted(MINIMATCH));
  const w = warnings(out);
  assert.equal(w.length, 1, out.join('\n'));
  assert.match(w[0], /GHSA-35jh-r3h4-6jhm/);
  assert.match(w[0], /lodash/);
  assert.doesNotMatch(w[0], new RegExp(MINIMATCH));
});

test('one advisory in several packages warns once, naming them all', () => {
  const a = advisory('GHSA-aaaa-bbbb-cccc', 'x', 'shared');
  const out = check(report(['x', [a]], ['y', [{ ...a, name: 'y' }]]), accepted());
  const w = warnings(out);
  assert.equal(w.length, 1, out.join('\n'));
  assert.match(w[0], /x, y/);
});

test('an accepted advisory npm no longer reports is a notice, not a warning', () => {
  const out = check(report(), accepted(MINIMATCH));
  assert.deepEqual(warnings(out), []);
  assert.equal(notices(out).length, 1);
  assert.match(notices(out)[0], new RegExp(MINIMATCH));
});

test('no advisories and none accepted: nothing to say', () => {
  const out = check(report(), accepted());
  assert.deepEqual(warnings(out), []);
  assert.deepEqual(notices(out), []);
});

test('an audit that gave no report (the registry failed) warns', () => {
  for (const text of ['', 'not json', JSON.stringify({ error: { code: 'ENOTFOUND', summary: 'request failed' } })]) {
    const w = warnings(check(text, accepted(MINIMATCH)));
    assert.equal(w.length, 1, JSON.stringify(text));
    assert.match(w[0], /no report/);
  }
});

// Titles as a hostile registry could send them, each aimed at the runner's command parsing.
const HOSTILE = [
  'first ##[stop-commands]zzzzzzzz',
  `##[add-mask]${MINIMATCH}`,
  'lone CR\r::add-mask::GHSA-x',
  'tab\n\t::error::injected',
  'line\n::stop-commands::x\r\n::add-mask::y 100%',
];

test("the registry's text runs no workflow command: only our stop, resume and annotations do", () => {
  const evil = HOSTILE.map((title, i) => [`evil${i}`, [advisory(`GHSA-dddd-eeee-ff0${i}`, `evil${i}`, title)]]);
  const out = check(report(minimatch, ...evil), accepted(MINIMATCH)).join('\n');
  const ran = commands(out);
  assert.deepEqual(ran.map((c) => c.name), ['stop-commands', 'resume', ...HOSTILE.map(() => 'warning')], out);
  for (const c of ran.slice(2)) {
    assert.match(c.line, /^::warning title=npm audit::New advisory, not in web\/audit-accepted\.json: GHSA-dddd-eeee-ff0/);
    assert.doesNotMatch(c.line, /##\[/, c.line); // v2 wins over v1 today; an annotation never carries the v1 form anyway
  }
  assert.match(out, /line%0A::stop-commands::x%0D%0A::add-mask::y 100%25/); // escaped, so the annotation keeps it
});

test('the listing runs inside a stop of its own, with a fresh token each time', () => {
  const first = (out) => out[0];
  const a = check(report(minimatch), accepted(MINIMATCH));
  assert.match(first(a), /^::stop-commands::[0-9a-f]{32}$/);
  assert.notEqual(first(a), first(check(report(minimatch), accepted(MINIMATCH))));
});

test('a broken list of accepted advisories fails loudly', () => {
  const list = (advisories) => JSON.stringify({ advisories });
  for (const [message, text] of [
    [/JSON/, 'not json'],
    [/must map each advisory ID/, list([MINIMATCH])],
    [/"minimatch" is not a GHSA ID/, list({ minimatch: 'development only' })],
    [/is not a GHSA ID/, list({ [`${MINIMATCH} minimatch`]: 'development only' })],
    [/needs the reason/, list({ [MINIMATCH]: '' })],
    [/needs the reason/, list({ [MINIMATCH]: '  ' })],
    [/needs the reason/, list({ [MINIMATCH]: true })],
  ]) {
    assert.throws(() => check(report(minimatch), text), message, text);
  }
});

test("npm's own advisory numbers can be accepted too", () => {
  const noGhsa = { source: 1234, name: 'x', title: 'no GHSA', severity: 'low' };
  const out = check(report(['x', [noGhsa]]), JSON.stringify({ advisories: { 'npm-1234': 'development only' } }));
  assert.deepEqual(warnings(out), []);
});

test("the repository's accepted advisories read, each with why it's accepted", () => {
  const text = readFileSync(new URL('../audit-accepted.json', import.meta.url), 'utf8');
  assert.deepEqual(warnings(check(report(), text)), []);
  assert.ok(Object.keys(JSON.parse(text).advisories).length > 0);
});

test('advisories without a GHSA ID or npm number stay apart', () => {
  const a = { name: 'x', title: 'one', url: 'https://example.org/a', severity: 'low' };
  const b = { name: 'x', title: 'two', url: 'https://example.org/b', severity: 'low' };
  assert.equal(warnings(check(report(['x', [a, b]]), accepted())).length, 2);
});
