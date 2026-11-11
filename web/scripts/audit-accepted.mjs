// Compares npm audit's report with the advisories we have accepted (web/audit-accepted.json, #115), so the vuln
// workflow warns only when something changes: an advisory not accepted is a warning, an accepted one npm no longer
// reports is a notice (drop it from the list). Every advisory is listed. This never fails on an advisory: the next
// step, `npm audit --omit=dev`, fails on any in a package that ships, accepted or not.
//
//   npm audit --json | node scripts/audit-accepted.mjs audit-accepted.json
import { randomBytes } from 'node:crypto';
import { readFileSync, realpathSync } from 'node:fs';
import { argv, exit, stderr, stdin, stdout } from 'node:process';
import { fileURLToPath } from 'node:url';

// Text from the registry goes into the log, and the runner reads a command from a line that starts with "::" after
// any spaces, or that holds "##[" anywhere (lines end at \n, \r\n or a lone \r). So the plain listing goes out with
// commands stopped (a token of its own each run), and the annotations come after it with the runner's escapes for
// line breaks and every "##[" broken up.
const defang = (s) => s.replaceAll('##[', '## [');
const escapeData = (s) => s.replaceAll('%', '%25').replaceAll('\r', '%0D').replaceAll('\n', '%0A');
const oneLine = (s) => s.replace(/[\r\n]+/g, ' ');
const warning = (s) => `::warning title=npm audit::${escapeData(s)}`;
const notice = (s) => `::notice title=npm audit::${escapeData(s)}`;

// The advisory's GHSA ID from its URL, or npm's own number when it has none (its URL, failing that).
const idOf = (via) =>
  (typeof via.url === 'string' && /GHSA(-[0-9a-z]{4}){3}$/.exec(via.url)?.[0]) || (via.source !== undefined ? `npm-${via.source}` : String(via.url ?? via.title));

// The IDs idOf gives that can be accepted: a GHSA ID, or npm's own number.
const ACCEPTABLE_ID = /^(GHSA(-[0-9a-z]{4}){3}|npm-[0-9]+)$/;

function readAccepted(text) {
  const advisories = JSON.parse(text).advisories;
  if (!advisories || typeof advisories !== 'object' || Array.isArray(advisories)) {
    throw new Error('audit-accepted.json: "advisories" must map each advisory ID to why it is accepted');
  }
  for (const [id, why] of Object.entries(advisories)) {
    if (!ACCEPTABLE_ID.test(id)) throw new Error(`audit-accepted.json: ${JSON.stringify(id)} is not a GHSA ID or npm-<number>`);
    if (typeof why !== 'string' || !why.trim()) throw new Error(`audit-accepted.json: ${id} needs the reason it is accepted`);
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

/** The log's lines for npm audit's JSON report `reportText`, against audit-accepted.json's text `acceptedText`. */
export function check(reportText, acceptedText) {
  const known = readAccepted(acceptedText);
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

  const listing = [];
  const annotations = [];
  for (const [id, a] of [...found].sort(([x], [y]) => x.localeCompare(y))) {
    const what = defang(`${id} (${a.severity}) in ${[...a.packages].sort().join(', ')}: ${a.title}`);
    listing.push(oneLine(`${known.has(id) ? 'known' : 'NEW'}  ${what}`));
    if (!known.has(id)) annotations.push(warning(`New advisory, not in web/audit-accepted.json: ${what}`));
  }
  for (const id of [...known].sort()) {
    if (!found.has(id)) annotations.push(notice(`${id} is no longer reported: drop it from web/audit-accepted.json`));
  }
  if (found.size === 0) listing.push('No advisories.');
  const token = randomBytes(16).toString('hex');
  return [`::stop-commands::${token}`, ...listing, `::${token}::`, ...annotations];
}

// Run as a script (argv[1] resolved, since import.meta.url is the real path even when the checkout's path has a symlink).
if (argv[1] && realpathSync(argv[1]) === fileURLToPath(import.meta.url)) {
  if (stdin.isTTY) {
    stderr.write('usage: npm audit --json | node scripts/audit-accepted.mjs [audit-accepted.json]\n');
    exit(2);
  }
  const chunks = [];
  for await (const chunk of stdin) chunks.push(chunk);
  const lines = check(Buffer.concat(chunks).toString('utf8'), readFileSync(argv[2] ?? 'audit-accepted.json', 'utf8'));
  stdout.write(lines.join('\n') + '\n');
}
