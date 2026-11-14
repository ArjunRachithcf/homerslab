// Third-party text in CI's log (#115): a registry's or a vulnerability database's text runs with workflow commands
// stopped (.github/scripts/stop-commands.sh), so it can't make annotations or masks. Run with `npm run test:unit`.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { parseDocument, visit } from 'yaml';

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

// A workflow, parsed. An anchor or an alias is refused: it could put a setting where the checks below don't look.
function parseWorkflow(text) {
  const doc = parseDocument(text, { merge: false });
  assert.deepEqual(doc.errors.map(String), []);
  visit(doc, {
    Alias() { throw new Error('a workflow with an alias'); },
    Node(_, node) { if (node.anchor) throw new Error('a workflow with an anchor'); },
  });
  return doc.toJS();
}
const jobs = () => Object.entries(parseWorkflow(workflow('ci.yml')).jobs);

const GO_CMD = /\bgo (-C \S+ )?(vet|test|build|run|install|generate|list|get|tool|mod)\b/;
// Settings that let go reach a source without the proxy off (GONOPROXY=* with GOPROXY=off still fetches over git),
// skip the checksum database, or make the shell read a file before every command.
const BAN = /GONOPROXY|GOPRIVATE|GONOSUMDB|GONOSUMCHECK|GOINSECURE|GOSUMDB|BASH_ENV/;
const FETCH_RUN = /^sh (\.\.\/)?\.github\/scripts\/stop-commands\.sh go mod (download|tidy -diff)$/;
const GOPROXY_ADDRESS = 'https://proxy.golang.org,direct';
const isGoJob = (job) => GO_CMD.test(JSON.stringify(job));

// A shell that is not exactly bash or sh could run something else first (`bash -c "go test && bash {0}"`).
const shellProblems = (defaults) => {
  const shell = defaults?.run?.shell;
  return shell === undefined || shell === 'bash' || shell === 'sh' ? [] : ['a default shell other than bash or sh'];
};

// What is wrong with the workflow's own env: and defaults:, which every job inherits; [] when nothing. Only
// GOFLAGS: -buildvcs=false may be there, and no GOPROXY (each Go job sets its own).
function workflowProblems(wf) {
  const problems = shellProblems(wf.defaults);
  if (wf.env !== undefined && (typeof wf.env !== 'object' || wf.env === null || Array.isArray(wf.env))) problems.push('an env that is not a map');
  const text = JSON.stringify({ env: wf.env, defaults: wf.defaults });
  if (BAN.test(text)) problems.push('a setting that bypasses the proxy, the sums or the shell');
  if (/GOPROXY/.test(text)) problems.push('GOPROXY set for every job');
  const flags = wf.env?.GOFLAGS === '-buildvcs=false' ? 1 : 0;
  if ((text.match(/GOFLAGS/g) ?? []).length !== flags) problems.push('GOFLAGS other than -buildvcs=false');
  return problems;
}

// What is wrong with a parsed Go job; [] when nothing. GOPROXY appears only as the job's "off" and as the fetch
// step's address; the fetch step has exactly a name, that env and the wrapper line as its run, and comes first.
function goJobProblems(job) {
  const problems = shellProblems(job.defaults);
  const steps = Array.isArray(job.steps) ? job.steps : [];
  const envs = [job.env, job.container?.env, ...steps.map((step) => step.env)].filter((env) => env !== undefined);
  if (envs.some((env) => typeof env !== 'object' || env === null || Array.isArray(env))) problems.push('an env that is not a map');
  if (job.env?.GOPROXY !== 'off') problems.push('GOPROXY not off for the job');
  const text = JSON.stringify(job);
  if (BAN.test(text)) problems.push('a setting that bypasses the proxy, the sums or the shell');
  if ((text.match(/GOPROXY/g) ?? []).length !== 2) problems.push('GOPROXY appears where only the job and the fetch step may set it');
  const flags = envs.filter((env) => env && typeof env === 'object' && env.GOFLAGS === '-buildvcs=false').length;
  if ((text.match(/GOFLAGS/g) ?? []).length !== flags) problems.push('GOFLAGS other than -buildvcs=false');
  const fetches = steps.filter((step) => step.env && typeof step.env === 'object' && 'GOPROXY' in step.env);
  const fetch = fetches[0];
  if (fetches.length !== 1) problems.push('not exactly one fetch step');
  else if (Object.keys(fetch).sort().join() !== 'env,name,run' || typeof fetch.name !== 'string'
    || Object.keys(fetch.env).join() !== 'GOPROXY' || fetch.env.GOPROXY !== GOPROXY_ADDRESS
    || typeof fetch.run !== 'string' || !FETCH_RUN.test(fetch.run)) problems.push('the fetch step is not exactly the wrapper line');
  const firstGo = steps.findIndex((step) => step !== fetch && GO_CMD.test(JSON.stringify(step)));
  if (firstGo >= 0 && steps.indexOf(fetch) > firstGo) problems.push('a Go step before the fetch');
  return problems;
}

// A job from text, for the cases below.
const FETCH_STEP = `      - name: fetch
        env:
          GOPROXY: https://proxy.golang.org,direct
        run: sh .github/scripts/stop-commands.sh go mod download
`;
// `top` is the workflow's own keys, `defaults` the job's.
const problemsOf = ({ top = '', env = '      GOPROXY: "off"\n', defaults = '', fetch = FETCH_STEP, steps = '      - run: go test ./...\n' } = {}) => {
  const wf = parseWorkflow(`${top}jobs:\n  j:\n    env:\n${env}${defaults}    steps:\n${fetch}${steps}`);
  return [...workflowProblems(wf), ...goJobProblems(wf.jobs.j)];
};

test('a Go command is found by every verb and form the filter lists, and not by a lookalike', () => {
  for (const verb of ['vet', 'test', 'build', 'run', 'install', 'generate', 'list', 'get', 'tool', 'mod verify'])
    assert.ok(isGoJob({ run: `go ${verb} ./...` }), verb);
  assert.ok(isGoJob({ run: 'go -C tools build ./x' }));
  assert.ok(!isGoJob({ run: 'gopher' }));
});

test('a Go job that is as it should be has no problem', () => {
  assert.deepEqual(problemsOf(), []);
});

test('a Go job that leaks the proxy setting is caught, in every form', () => {
  const step = (extra) => `      - run: go test ./...\n${extra}`;
  const cases = {
    'GONOPROXY inline': '      - run: GOPROXY=off GONOPROXY=* go get x\n',
    'GONOPROXY in a step env': step('        env:\n          GONOPROXY: "*"\n'),
    'GOPRIVATE': step('        env:\n          GOPRIVATE: x\n'),
    'GONOSUMDB': step('        env:\n          GONOSUMDB: x\n'),
    'GOINSECURE': step('        env:\n          GOINSECURE: x\n'),
    'GOSUMDB': step('        env:\n          GOSUMDB: off\n'),
    'GOFLAGS -insecure': step('        env:\n          GOFLAGS: -insecure\n'),
    'GOPROXY in a later step': step('        env:\n          GOPROXY: https://x\n'),
    'GOPROXY as a quoted key': step('        env:\n          "GOPROXY": x\n'),
    'GOPROXY with a space before the colon': step('        env:\n          GOPROXY : x\n'),
    'a heredoc into GITHUB_ENV': '      - run: |\n          GOPROXY<<X >> "$GITHUB_ENV"\n          https://x\n          X\n',
    'printf -v': '      - run: printf -v GOPROXY x\n',
    'BASH_ENV in a step': step('        env:\n          BASH_ENV: /tmp/x\n'),
  };
  for (const [name, steps] of Object.entries(cases)) assert.notDeepEqual(problemsOf({ steps }), [], name);
  assert.notDeepEqual(problemsOf({ env: '      GOPROXY: "off"\n      BASH_ENV: /tmp/x\n' }), [], 'BASH_ENV for the job');
  assert.notDeepEqual(problemsOf({ env: '      GOPROXY: "off"\n      GONOPROXY: "*"\n' }), [], 'GONOPROXY for the job');
  assert.notDeepEqual(problemsOf({ env: '      GOPROXY: https://x\n' }), [], 'the job with the proxy on');
  assert.deepEqual(problemsOf({ env: '      GOPROXY: "off"\n      GOFLAGS: -buildvcs=false\n' }), []);
});

test('the workflow-level env and defaults, and the job-level defaults, are checked too', () => {
  const top = {
    'GONOPROXY for every job': 'env:\n  GONOPROXY: "*"\n',
    'BASH_ENV for every job': 'env:\n  BASH_ENV: /tmp/x\n',
    'GONOSUMDB for every job': 'env:\n  GONOSUMDB: x\n',
    'GOFLAGS -mod=mod for every job': 'env:\n  GOFLAGS: -mod=mod\n',
    'GOPROXY for every job': 'env:\n  GOPROXY: https://x\n',
    'a shell that runs go first': 'defaults:\n  run:\n    shell: bash -c "go test ./... && bash {0}"\n',
    'a shell with options': 'defaults:\n  run:\n    shell: bash -e {0}\n',
  };
  for (const [name, text] of Object.entries(top)) assert.notDeepEqual(problemsOf({ top: text }), [], name);
  assert.notDeepEqual(problemsOf({ defaults: '    defaults:\n      run:\n        shell: bash -c "go test ./... && bash {0}"\n' }), [], 'a job shell');
  assert.deepEqual(problemsOf({ top: 'env:\n  GOFLAGS: -buildvcs=false\n' }), []);
  assert.deepEqual(problemsOf({ top: 'defaults:\n  run:\n    shell: bash\n' }), []);
  assert.deepEqual(problemsOf({ defaults: '    defaults:\n      run:\n        shell: sh\n' }), []);
});

test('the fetch step is exactly name, the address and the wrapper line, and comes first', () => {
  const fetch = (run, extra = '') => `      - name: fetch\n        env:\n          GOPROXY: https://proxy.golang.org,direct\n${extra}        run: ${run}\n`;
  const wrapper = 'sh .github/scripts/stop-commands.sh go mod download';
  const cases = {
    'a run: | block with go test after it': fetch(`|\n          ${wrapper}\n          go test ./...`),
    'a continued plain scalar': fetch(`${wrapper}\n          go test ./...`),
    'a lookalike that only mentions the wrapper': fetch('go vet ./... # stop-commands.sh go mod download'),
    'a shell: on it': fetch(wrapper, '        shell: bash {0}\n'),
    'an if: on it': fetch(wrapper, '        if: always()\n'),
    'a working-directory: on it': fetch(wrapper, '        working-directory: x\n'),
    'a different go mod': fetch('sh .github/scripts/stop-commands.sh go mod download all'),
    'no wrapper': fetch('go mod download'),
    'no name': '      - env:\n          GOPROXY: https://proxy.golang.org,direct\n        run: sh .github/scripts/stop-commands.sh go mod download\n',
    'another address': fetch(wrapper).replace('proxy.golang.org,direct', 'example.com'),
  };
  for (const [name, text] of Object.entries(cases)) assert.notDeepEqual(problemsOf({ fetch: text }), [], name);
  assert.notDeepEqual(problemsOf({ fetch: '' }), [], 'no fetch step');
  assert.notDeepEqual(problemsOf({ fetch: '', steps: `      - run: go test ./...\n${FETCH_STEP}` }), [], 'the fetch after a Go step');
  assert.notDeepEqual(problemsOf({ steps: `${FETCH_STEP}` }), [], 'two fetch steps');
});

test('a workflow with an anchor or an alias is refused', () => {
  assert.throws(() => parseWorkflow('jobs:\n  j:\n    env: &e\n      GOPROXY: "off"\n    steps: []\n'), /anchor/);
  assert.throws(() => parseWorkflow('a: &x 1\nb: *x\n'), /a(n)? (anchor|alias)/);
  assert.throws(() => parseWorkflow('jobs:\n  j:\n    env:\n      <<: *x\n'));
});

test('every Go job fetches its modules under the wrapper first, then runs with the proxy off', () => {
  // A text lint: it can't see a value built at run time, or set in a helper script or a Makefile.
  assert.deepEqual(workflowProblems(parseWorkflow(workflow('ci.yml'))), []);
  for (const [id, job] of jobs()) assert.deepEqual(shellProblems(job.defaults), [], id);
  const goJobs = jobs().filter(([, job]) => isGoJob(job));
  assert.ok(goJobs.length >= 5, goJobs.map(([id]) => id).join(' '));
  for (const [id, job] of goJobs) {
    assert.deepEqual(goJobProblems(job), [], id);
    // `go mod download all` writes sums into go.sum that go mod tidy -diff then refuses.
    assert.doesNotMatch(JSON.stringify(job), /go mod download all/, `${id}: leaves go.sum as it is`);
  }
});

// A suite that drives containers prints their logs when it fails.
test('every suite that drives containers runs under the wrapper', () => {
  const suites = jobs().filter(([, job]) => JSON.stringify(job).includes('HOMERSLAB_TEST_DOCKER_HOST'));
  assert.ok(suites.length >= 2);
  for (const [id, job] of suites) {
    const runs = job.steps.map((step) => step.run).filter((run) => typeof run === 'string' && /\bgo test\b/.test(run));
    assert.ok(runs.length > 0, id);
    for (const run of runs) assert.match(run, /^sh \.github\/scripts\/stop-commands\.sh go test /, `${id}: ${run}`);
  }
});
