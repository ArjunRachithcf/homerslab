// The setup page's requests wait for the manager's answer for a bounded time, then give up as Unreachable (#268): a
// manager that never answers leaves the page saying so, never a button that spins forever. Run with `npm run test:unit`.
import assert from 'node:assert/strict';
import { afterEach, beforeEach, mock, test } from 'node:test';

import { answer, claim, getPrimary, getSetup, Unreachable, WAIT, type StepState } from './api.ts';

const step = (id: string): StepState => ({ id, half: 'lan', state: 'todo', revision: 1 });

/** A manager that never answers: the request stays open until the page gives up on it, if it honours the signal. */
function silent({ honours = true } = {}) {
  const asked: RequestInit[] = [];
  globalThis.fetch = ((_: string, init: RequestInit) => {
    asked.push(init);
    return new Promise<Response>((_resolve, reject) => {
      if (honours) init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
    });
  }) as typeof fetch;
  return asked;
}

/** A manager that sends its answer's headers, then never the body. */
function mute() {
  globalThis.fetch = ((_: string, init: RequestInit) => Promise.resolve({
    ok: true, status: 200,
    json: () => new Promise((_resolve, reject) => init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))),
  } as Response)) as typeof fetch;
}

/** Where a promise stands now, after the microtasks the fake clock's tick let run. */
async function standing(p: Promise<unknown>): Promise<'waiting' | 'unreachable' | 'other'> {
  const seen = p.then(() => 'other' as const, (e: unknown) => (e instanceof Unreachable ? 'unreachable' as const : 'other' as const));
  for (let i = 0; i < 20; i++) await Promise.resolve();
  return Promise.race([seen, Promise.resolve('waiting' as const)]);
}

const fetched = globalThis.fetch;
beforeEach(() => mock.timers.enable({ apis: ['setTimeout'] }));
afterEach(() => {
  mock.timers.reset();
  globalThis.fetch = fetched;
});

const kinds: { name: string; send: () => Promise<unknown>; limit: number }[] = [
  { name: 'reading setup', send: getSetup, limit: WAIT.read },
  { name: 'reading the machine’s facts', send: getPrimary, limit: WAIT.read },
  { name: 'claiming the link', send: () => claim({ key: 'k', nonce: 'n'.repeat(22) }), limit: WAIT.claim },
  { name: 'answering a step', send: () => answer(step('you'), { name: 'Inês' }), limit: WAIT.answer },
  { name: 'answering the network step, which joins the mesh', send: () => answer(step('network'), { mesh: 'netbird' }), limit: WAIT.network },
];

for (const k of kinds) {
  test(`${k.name} that gets no answer gives up as Unreachable after ${k.limit / 1000} s, and not before`, async () => {
    const asked = silent();
    const p = k.send();
    mock.timers.tick(k.limit - 1);
    assert.equal(await standing(p), 'waiting');
    mock.timers.tick(1);
    assert.equal(await standing(p), 'unreachable');
    assert.equal(asked[0]?.signal?.aborted, true, 'the request itself is cancelled, not left open');
  });
}

test('a request gives up even when the browser doesn’t end the fetch on the signal', async () => {
  silent({ honours: false });
  const p = answer(step('you'), { name: 'Inês' });
  mock.timers.tick(WAIT.answer);
  assert.equal(await standing(p), 'unreachable');
});

test('an answer whose body never comes gives up as Unreachable too', async () => {
  mute();
  const p = getSetup();
  mock.timers.tick(WAIT.read);
  assert.equal(await standing(p), 'unreachable');
});

test('a claim and its three tries again (1, 2 and 4 s apart, App.tsx) end inside the manager’s minute for the same claim', () => {
  assert.ok(4 * WAIT.claim + 7_000 < 60_000); // internal/manager/setup: ClaimAgainWithin
});

test('an answer whose body is cut off is no answer: Unreachable, not an empty setup', async () => {
  globalThis.fetch = (() => Promise.resolve(new Response('{"half":"la', { status: 200 }))) as typeof fetch;
  assert.equal(await standing(getSetup()), 'unreachable');
});

test('the limits: a quick read, a step’s answer, and the network step a little past the setup listener’s 3 minutes', () => {
  assert.ok(WAIT.read <= WAIT.answer);
  assert.ok(WAIT.answer <= 60_000);
  assert.ok(WAIT.network > 3 * 60_000, 'longer than the manager takes to join the mesh (its WriteTimeout)');
  assert.ok(WAIT.network <= 4 * 60_000);
});

test('an answer in time stops the clock: nothing gives up on it later', async () => {
  globalThis.fetch = (() => Promise.resolve(new Response(JSON.stringify({ half: 'lan', current: 'you', steps: [], answers: {} }),
    { status: 200, headers: { 'Content-Type': 'application/json' } }))) as typeof fetch;
  const late: unknown[] = [];
  const onLate = (e: unknown) => late.push(e);
  process.on('unhandledRejection', onLate);
  try {
    assert.equal((await getSetup()).current, 'you');
    mock.timers.tick(WAIT.network);
    await standing(Promise.resolve());
    assert.deepEqual(late, []);
  } finally {
    process.off('unhandledRejection', onLate);
  }
});
