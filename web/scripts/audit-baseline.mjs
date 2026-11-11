// Compares npm audit's report with the advisories we already know of (web/audit-baseline.json, #115), so the vuln
// workflow warns only when something changes: an advisory not in the baseline is a warning, a baseline advisory npm
// no longer reports is a notice (drop it from the baseline). Every advisory is listed. This never fails on an
// advisory: the next step, `npm audit --omit=dev`, fails on any in a package that ships, baseline or not.
//
//   npm audit --json | node scripts/audit-baseline.mjs audit-baseline.json
import { readFileSync } from 'node:fs';
import { argv, stdin, stdout } from 'node:process';
import { fileURLToPath } from 'node:url';

// Text from the registry goes into the log. A line break in it could start a workflow command of its own, so
// annotations get the runner's escapes and plain lines lose their breaks.
const escapeData = (s) => s.replaceAll('%', '%25').replaceAll('\r', '%0D').replaceAll('\n', '%0A');
const oneLine = (s) => s.replace(/[\r\n]+/g, ' ');
const warning = (s) => `::warning title=npm audit::${escapeData(s)}`;
const notice = (s) => `::notice title=npm audit::${escapeData(s)}`;

// The advisory's GHSA ID from its URL, or npm's own number when it has none.
const idOf = (via) => (typeof via.url === 'string' && /GHSA(-[0-9a-z]{4}){3}$/.exec(via.url)?.[0]) || `npm-${via.source}`;

function readBaseline(text) {
  const advisories = JSON.parse(text).advisories;
  if (!advisories || typeof advisories !== 'object' || Array.isArray(advisories)) {
    throw new Error('audit-baseline.json: "advisories" must map each advisory ID to why it is accepted');
  }
  return new Set(Object.keys(advisories));
}

function readReport(text) {
  try {
    const vulnerabilities = JSON.parse(text).vulnerabilities;
    return vulnerabilities && typeof vulnerabilities === 'object' ? vulnerabilities : null;
  } catch {
    return null;
  }
}

/** The log's lines for npm audit's JSON report `reportText`, against the baseline file's text `baselineText`. */
export function check(reportText, baselineText) {
  const known = readBaseline(baselineText);
  const vulnerabilities = readReport(reportText);
  if (!vulnerabilities) {
    return [warning('npm audit gave no report (the registry failed?): see the log. The next step still fails on an advisory in a package that ships.')];
  }

  const found = new Map();
  for (const entry of Object.values(vulnerabilities)) {
    for (const via of entry.via ?? []) {
      if (typeof via !== 'object' || via === null) continue; // the name of a package it arrives through
      const id = idOf(via);
      const a = found.get(id) ?? { title: String(via.title ?? ''), severity: String(via.severity ?? ''), packages: new Set() };
      a.packages.add(String(via.name ?? entry.name));
      found.set(id, a);
    }
  }

  const out = [];
  for (const [id, a] of [...found].sort(([x], [y]) => x.localeCompare(y))) {
    const what = `${id} (${a.severity}) in ${[...a.packages].sort().join(', ')}: ${a.title}`;
    out.push(oneLine(`${known.has(id) ? 'known' : 'NEW'}  ${what}`));
    if (!known.has(id)) out.push(warning(`New advisory, not in web/audit-baseline.json: ${what}`));
  }
  for (const id of [...known].sort()) {
    if (!found.has(id)) out.push(notice(`${id} is no longer reported: drop it from web/audit-baseline.json`));
  }
  if (found.size === 0) out.push('No advisories.');
  return out;
}

if (argv[1] === fileURLToPath(import.meta.url)) {
  const chunks = [];
  for await (const chunk of stdin) chunks.push(chunk);
  const lines = check(Buffer.concat(chunks).toString('utf8'), readFileSync(argv[2] ?? 'audit-baseline.json', 'utf8'));
  stdout.write(lines.join('\n') + '\n');
}
