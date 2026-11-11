// Third-party text in CI's log (#115): a registry's or a vulnerability database's text runs with workflow commands
// stopped (.github/scripts/stop-commands.sh), so it can't make annotations or masks. Run with `npm run test:unit`.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const script = fileURLToPath(new URL('../../.github/scripts/stop-commands.sh', import.meta.url));
const workflow = (name) => readFileSync(new URL(`../../.github/workflows/${name}`, import.meta.url), 'utf8');
const wrapped = (shell) => spawnSync('sh', [script, 'sh', '-c', shell], { encoding: 'utf8' });

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

// The steps whose output carries text from a registry, a package's author or a vulnerability database.
const THIRD_PARTY = /\b(npm (audit|ci)|govulncheck -show|playwright install|go -C tools (mod|build))\b/;

for (const name of ['vuln.yml', 'ci.yml']) {
  test(`every command in ${name} that prints third-party text runs with workflow commands stopped`, () => {
    const commands = workflow(name).split('\n').map((l) => l.trim())
      .filter((l) => !/^(#|echo |(- )?name:)/.test(l) && THIRD_PARTY.test(l)); // our own words aside
    assert.ok(commands.length > 0);
    for (const line of commands) assert.match(line, /stop-commands\.sh /, line);
  });
}
