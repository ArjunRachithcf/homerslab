// Third-party text in CI's log (#115): a registry's or a vulnerability database's text runs with workflow commands
// stopped (.github/scripts/stop-commands.sh), so it can't make annotations or masks. Run with `npm run test:unit`.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

import { commands } from './runner-commands.mjs';

const script = fileURLToPath(new URL('../../.github/scripts/stop-commands.sh', import.meta.url));
const workflow = (name) => readFileSync(new URL(`../../.github/workflows/${name}`, import.meta.url), 'utf8');
const wrapped = (shell, env = process.env) => spawnSync('sh', [script, 'sh', '-c', shell], { encoding: 'utf8', env });

test("the command's output, its errors included, sits between stopping the commands and starting them again", () => {
  const r = wrapped('echo "::warning title=spoof::out"; echo "::add-mask::err" >&2; echo "::error::last"');
  const lines = r.stdout.trimEnd().split('\n');
  const token = /^::stop-commands::([0-9a-f]{32})$/.exec(lines[0])?.[1];
  assert.ok(token, r.stdout);
  assert.equal(lines.at(-1), `::${token}::`);
  assert.deepEqual(lines.slice(1, -1).filter(Boolean), ['::warning title=spoof::out', '::add-mask::err', '::error::last']);
  assert.equal(r.stderr, '');
});

test('commands start again on a line of their own, even after output without a final line break', () => {
  const lines = wrapped('printf "::warning::no newline"').stdout.split('\n');
  const token = lines[0].split('::')[2];
  assert.ok(lines.includes(`::${token}::`), lines.join('\n'));
});

test('each run stops the commands with a token of its own', () => {
  const first = (r) => r.stdout.split('\n')[0];
  assert.notEqual(first(wrapped('true')), first(wrapped('true')));
});

test("the command's exit status is kept", () => {
  for (const status of [0, 1, 3]) assert.equal(wrapped(`exit ${status}`).status, status);
});

test("as the runner reads it, the command's output runs no command, however it's dressed", () => {
  const r = wrapped(String.raw`echo "first ##[stop-commands]zzzzzzzz"; printf 'lone CR\r::add-mask::x\n'; printf '\t::error::tab\n'; echo "##[add-mask]y" >&2`);
  assert.deepEqual(commands(r.stdout).map((c) => c.name), ['stop-commands', 'resume'], r.stdout);
});

test('without a whole token it runs nothing and fails', () => {
  const bin = mkdtempSync(path.join(tmpdir(), 'stop-commands-'));
  try {
    writeFileSync(path.join(bin, 'od'), '#!/bin/sh\nexit 0\n'); // an od that gives no bytes
    chmodSync(path.join(bin, 'od'), 0o755);
    const r = wrapped('echo ran', { ...process.env, PATH: `${bin}:${process.env.PATH}` });
    assert.equal(r.status, 1);
    assert.doesNotMatch(r.stdout, /ran/);
  } finally {
    rmSync(bin, { recursive: true, force: true });
  }
});

// The commands whose output carries text from a registry, a package's author, a mirror, a vulnerability database or
// a container's logs.
const THIRD_PARTY = /(npm (audit|ci)|npx playwright install|govulncheck -show|go -C tools (mod|build)|go mod download|sudo apt-get|go test [^']*TestTheMeshContract)/g;
const WRAPPER = /\bsh (\.\.\/)?\.github\/scripts\/stop-commands\.sh (sh -c ')?$/;

for (const name of ['vuln.yml', 'ci.yml']) {
  test(`every command in ${name} that prints third-party text runs under the wrapper`, () => {
    const lines = workflow(name).split('\n').map((l) => l.trim()).filter((l) => !/^(#|echo |(- )?name:)/.test(l)); // our own words aside
    let seen = 0;
    for (const line of lines) {
      for (const m of line.matchAll(THIRD_PARTY)) {
        seen++;
        assert.match(line.slice(0, m.index), WRAPPER, line);
      }
    }
    assert.ok(seen > 0);
  });
}

test('every Go job fetches its modules under the wrapper first, then runs with the proxy off', () => {
  const ci = workflow('ci.yml');
  for (const job of ['go', 'firewall', 'mesh', 'flow']) {
    const body = new RegExp(`\\n  ${job}:\\n([\\s\\S]*?)(?=\\n  [a-z]+:\\n|$)`).exec(ci)?.[1] ?? '';
    assert.match(body, /\n {4}env:\n( {6}.*\n)* {6}GOPROXY: "off"\n/, `${job}: GOPROXY off for the job`);
    assert.match(body, /stop-commands\.sh go mod download/, `${job}: fetches its modules first`);
    const goSteps = body.split('\n      - ').filter((step) => /\bgo (vet|test|build|mod tidy)\b/.test(step) && !/go mod download/.test(step));
    assert.ok(goSteps.length > 0, job);
    for (const step of goSteps) assert.doesNotMatch(step, /GOPROXY: (?!"off")/, `${job}: a later step turns the proxy on`);
    const download = body.split('\n      - ').find((step) => /go mod download/.test(step)) ?? '';
    assert.ok(body.indexOf(download) < body.indexOf(goSteps[0]), `${job}: fetches before it builds`);
  }
});
