// The accepted advisories (#115): the npm audit job warns only for an advisory that isn't in web/audit-accepted.json.
// Run with `npm run test:unit` (node:test, nothing to install).
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

import { check } from './audit-accepted.mjs';

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

test("the registry's text can't start a workflow command or break an annotation", () => {
  const evil = advisory('GHSA-dddd-eeee-ffff', 'evil', 'title\n::stop-commands::x\r\n::add-mask::y 100%');
  const out = check(report(['evil', [evil]]), accepted());
  for (const line of out) {
    assert.doesNotMatch(line, /[\r\n]/, line);
  }
  assert.match(warnings(out)[0], /title%0A::stop-commands::x%0D%0A::add-mask::y 100%25/);
});

test('a broken list of accepted advisories fails loudly', () => {
  const list = (advisories) => JSON.stringify({ advisories });
  for (const [why, text] of [
    ['not JSON', 'not json'],
    ['an array', list([MINIMATCH])],
    ['a package name for an ID', list({ minimatch: 'development only' })],
    ['an ID with more after it', list({ [`${MINIMATCH} minimatch`]: 'development only' })],
    ['no reason', list({ [MINIMATCH]: '' })],
    ['a reason of spaces', list({ [MINIMATCH]: '  ' })],
    ['a reason that is not text', list({ [MINIMATCH]: true })],
  ]) {
    assert.throws(() => check(report(minimatch), text), Error, why);
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
