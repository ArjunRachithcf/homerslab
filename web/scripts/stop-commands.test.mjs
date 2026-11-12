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
    // an od that gives no bytes, and one that gives a token too short to be one
    for (const out of ['', '0a1b2c3d4e5f60718293a4b5c6d7e8f']) {
      writeFileSync(path.join(bin, 'od'), `#!/bin/sh\necho '${out}'\n`);
      chmodSync(path.join(bin, 'od'), 0o755);
      const r = wrapped('echo ran', { ...process.env, PATH: `${bin}:${process.env.PATH}` });
      assert.equal(r.status, 1, `token "${out}"`);
      assert.doesNotMatch(r.stdout, /ran/, `token "${out}"`);
    }
  } finally {
    rmSync(bin, { recursive: true, force: true });
  }
});

// The commands whose output carries text from a registry, a package's author, a mirror, a vulnerability database or
// a container's logs.
const THIRD_PARTY = /(npm (audit|ci)|npx playwright install|govulncheck -show|go -C tools (mod|build)|go mod (download|tidy)|sudo apt-get)/g;
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

// ci.yml's jobs, by id.
const jobs = () => {
  const ci = workflow('ci.yml');
  const at = [...ci.matchAll(/\n {2}([a-z][a-z0-9-]*):\n/g)];
  return at.map((m, i) => [m[1], ci.slice(m.index, at[i + 1]?.index ?? ci.length)]);
};

const GO_CMD = /\bgo (-C \S+ )?(vet|test|build|run|install|generate|list|get|tool|mod)\b/;
// Settings that let go reach a source without the proxy off (GONOPROXY=* with GOPROXY=off still fetches over git),
// or skip the checksum database, anywhere in a job: in env:, in a step, or inline in run: text.
const LEAKS = /\b(GONOPROXY|GOPRIVATE|GONOSUMDB|GONOSUMCHECK|GOINSECURE|GOSUMDB)\b|\bGOFLAGS\b(?!: -buildvcs=false\n)/; // GOFLAGS only as the jobs set it
const FETCH_RUN = /^sh (\.\.\/)?\.github\/scripts\/stop-commands\.sh go mod (download|tidy -diff)$/;

// The fetch step is the one step whose only run: is exactly the wrapper line, nothing else in it.
const isFetch = (step) => {
  const runs = [...step.matchAll(/^ *(?:- )?run:[ \t]*(.*)$/gm)];
  return runs.length === 1 && FETCH_RUN.test(runs[0][1]);
};

// What is wrong with a Go job's text; [] when nothing. GOPROXY may appear only as the job's `GOPROXY: "off"` and
// as the fetch step's `GOPROXY: https://proxy.golang.org,direct`: any other occurrence (a later step's env, inline,
// printf, a heredoc into $GITHUB_ENV) fails.
function goJobProblems(body) {
  const problems = [];
  if (!/\n {4}env:\n( {6}.*\n)* {6}GOPROXY: "off"\n/.test(body)) problems.push('GOPROXY not off for the job');
  if (LEAKS.test(body)) problems.push('a setting that bypasses the proxy or the sums');
  const [head, ...steps] = body.split('\n      - ');
  let rest = head.replace('\n      GOPROXY: "off"\n', '\n');
  for (const step of steps) {
    rest += '\n' + (isFetch(step) ? step.replace(/^ +GOPROXY: https:\/\/proxy\.golang\.org,direct$/m, '') : step);
  }
  if (/GOPROXY/.test(rest)) problems.push('GOPROXY appears where only the job and the fetch step may set it');
  return problems;
}

test('a Go command is found by every verb and form the filter lists, and not by a lookalike', () => {
  for (const verb of ['vet', 'test', 'build', 'run', 'install', 'generate', 'list', 'get', 'tool', 'mod verify'])
    assert.match(`run: go ${verb} ./...`, GO_CMD);
  assert.match('run: go -C tools build ./x', GO_CMD);
  assert.doesNotMatch('run: gopher', GO_CMD);
});

test('a Go job that leaks the proxy setting is caught, in every form', () => {
  const ok = '\n  job:\n    env:\n      GOPROXY: "off"\n    steps:\n      - run: go test ./...\n';
  assert.deepEqual(goJobProblems(ok), []);
  const bad = (text) => goJobProblems(ok.replace('      - run: go test ./...\n', text));
  assert.ok(bad('      - run: GOPROXY=off GONOPROXY=* go get x\n').length);
  assert.ok(bad('      - run: go get x\n        env:\n          GONOPROXY: "*"\n').length);
  assert.ok(bad('      - run: go test ./...\n        env:\n          GOPRIVATE: x\n').length);
  assert.ok(bad('      - run: go test ./...\n        env:\n          GOFLAGS: -insecure\n').length);
  assert.ok(bad('      - run: go test ./...\n        env:\n          GOPROXY: https://x\n').length);
  assert.ok(bad('      - run: go test ./...\n        env:\n          "GOPROXY": x\n').length);
  assert.ok(bad('      - run: go test ./...\n        env:\n          GOPROXY : x\n').length);
  assert.ok(bad('      - run: |\n          GOPROXY<<X >> "$GITHUB_ENV"\n          https://x\n          X\n').length);
  assert.ok(bad('      - run: printf -v GOPROXY x\n').length);
  const fetch = '      - env:\n          GOPROXY: https://proxy.golang.org,direct\n        run: sh .github/scripts/stop-commands.sh go mod tidy -diff\n';
  assert.deepEqual(bad(fetch), []);
  // the fetch step with more in its run: goes on with the proxy on
  assert.ok(bad(fetch.replace('run: sh', 'run: |\n          sh').concat('          go test ./...\n')).length);
  // a lookalike that only mentions the wrapper
  assert.ok(bad('      - env:\n          GOPROXY: https://proxy.golang.org,direct\n        run: go vet ./... # stop-commands.sh go mod download\n').length);
});

test('every Go job fetches its modules under the wrapper first, then runs with the proxy off', () => {
  const goJobs = jobs().filter(([, body]) => GO_CMD.test(body));
  assert.ok(goJobs.length >= 5, goJobs.map(([id]) => id).join(' '));
  for (const [job, body] of goJobs) {
    assert.deepEqual(goJobProblems(body), [], job);
    assert.match(body, /stop-commands\.sh go mod (download|tidy -diff)\n/, `${job}: fetches its modules first`);
    // `go mod download all` writes sums into go.sum that go mod tidy -diff then refuses.
    assert.doesNotMatch(body, /go mod download all/, `${job}: leaves go.sum as it is`);
    const steps = body.split('\n      - ').slice(1);
    const goSteps = steps.filter((step) => GO_CMD.test(step) && !isFetch(step));
    const fetch = steps.findIndex(isFetch);
    assert.ok(fetch >= 0 && fetch < steps.indexOf(goSteps[0]), `${job}: fetches before it builds`);
  }
});

// A suite that drives containers prints their logs when it fails.
test('every suite that drives containers runs under the wrapper', () => {
  const suites = jobs().filter(([, body]) => body.includes('HOMERSLAB_TEST_DOCKER_HOST'));
  assert.ok(suites.length >= 2);
  for (const [job, body] of suites) {
    const runs = body.split('\n').map((l) => l.trim()).filter((l) => /^run: .*\bgo test\b/.test(l));
    assert.ok(runs.length > 0, job);
    for (const run of runs) assert.match(run, /^run: sh \.github\/scripts\/stop-commands\.sh go test /, `${job}: ${run}`);
  }
});
