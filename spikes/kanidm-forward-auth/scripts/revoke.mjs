// Revocation timing, three paths, measured in parallel:
//  R1 app2 (client whoami2, its own oauth2-proxy): the client is deleted in Kanidm.
//  R2 app  (client whoami): a person is taken out of the role group (adults).
//  R3 app  (client whoami): the audience is narrowed by deleting the adults scope map.
import { chromium } from 'playwright';
import { readFileSync, writeFileSync, appendFileSync } from 'node:fs';
import { createHmac } from 'node:crypto';
import { execFileSync } from 'node:child_process';

const SPIKE = new URL('..', import.meta.url).pathname;
const IDM = 'https://idm.hlspike.test:28743';
const OUT = `${SPIKE}results/11-revocation.txt`;
writeFileSync(OUT, '');
const t0 = Date.now();
const ts = () => `[${new Date().toISOString().slice(11, 19)} +${((Date.now() - t0) / 1000).toFixed(0)}s]`;
const log = (...a) => { const s = a.join(' '); console.log(s); appendFileSync(OUT, s + '\n'); };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const creds = (p) => JSON.parse(readFileSync(`${SPIKE}secrets/${p}.json`, 'utf8'));
const py = (...args) => execFileSync('python3', ['-I', `${SPIKE}scripts/admin.py`, ...args], { encoding: 'utf8' }).trim();
function b32decode(s) {
  const a = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'; let bits = '';
  for (const c of s.replace(/=+$/, '')) bits += a.indexOf(c).toString(2).padStart(5, '0');
  const out = []; for (let i = 0; i + 8 <= bits.length; i += 8) out.push(parseInt(bits.slice(i, i + 8), 2));
  return Buffer.from(out);
}
const lastStep = {};
async function freshTotp(person) {
  const c = creds(person);
  let step = Math.floor(Date.now() / 1000 / c.step);
  if (lastStep[person] !== undefined && step <= lastStep[person]) {
    await sleep(((lastStep[person] + 1) * c.step * 1000) - Date.now() + 1500);
    step = Math.floor(Date.now() / 1000 / c.step);
  }
  lastStep[person] = step;
  const ctr = Buffer.alloc(8); ctr.writeBigUInt64BE(BigInt(step));
  const h = createHmac(c.algo, b32decode(c.totp_secret_b32)).update(ctr).digest();
  const o = h[h.length - 1] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 10 ** c.digits).padStart(c.digits, '0');
}
async function signIn(page, person) {
  for (let i = 0; i < 6 && page.url().startsWith(IDM); i++) {
    let f = null;
    for (const sel of ['username', 'totp', 'password']) {
      const el = page.locator('#' + sel);
      if (await el.count() && await el.isVisible()) { f = sel; break; }
    }
    if (!f) break;
    const v = f === 'username' ? person : f === 'totp' ? await freshTotp(person) : creds(person).password;
    try { await page.locator('#' + f).fill(v, { timeout: 5000 }); } catch { throw new Error(`fill ${f} failed`); }
    await Promise.all([page.waitForNavigation({ timeout: 20000 }).catch(() => {}),
      page.locator('button[type="submit"]:visible').first().click()]);
  }
}
async function probe(page, url) {
  const r = await page.goto(url);
  const ok = page.url() === url && r.status() === 200;
  return { ok, status: r.status(), at: page.url().split('?')[0] };
}
async function pollUntilDenied(name, page, url, everyMs, maxMs, since) {
  while (Date.now() - since < maxMs) {
    const p = await probe(page, url);
    const after = ((Date.now() - since) / 1000).toFixed(0);
    if (!p.ok) { log(`${ts()} ${name}: DENIED ${after}s after the change (HTTP ${p.status} at ${p.at})`); return Number(after); }
    log(`${ts()} ${name}: still 200, ${after}s after the change`);
    await sleep(everyMs);
  }
  log(`${ts()} ${name}: still allowed after ${(maxMs / 1000).toFixed(0)}s (stopped polling)`);
  return null;
}

const browser = await chromium.launch({ args: ['--host-resolver-rules=MAP *.hlspike.test 127.0.0.1'] });
const newCtx = () => browser.newContext({ ignoreHTTPSErrors: true });
const APP = 'https://app.hlspike.test:28744/api';
const APP2 = 'https://app2.hlspike.test:28744/api';

// R1: ada on app2, then delete client whoami2
const adaCtx = await newCtx();
const adaB = await adaCtx.newPage();
await adaB.goto(APP2); await signIn(adaB, 'ada');
await adaB.waitForURL((u) => !u.toString().startsWith(IDM), { timeout: 20000 }).catch(() => {});
log(`${ts()} R1: ada on app2 -> ${JSON.stringify(await probe(adaB, APP2))}`);
log(`${ts()} R1: ` + py('client-delete', 'whoami2').replace(/\n/g, ' | '));
const r1Since = Date.now();
const r1 = pollUntilDenied('R1 client deleted', adaB, APP2, 15000, 18 * 60 * 1000, r1Since);

// R2: ada3 on app, then remove ada3 from adults
const a3Ctx = await newCtx(); const a3 = await a3Ctx.newPage();
await a3.goto(APP); await signIn(a3, 'ada3');
await a3.waitForURL((u) => !u.toString().startsWith(IDM), { timeout: 20000 }).catch(() => {});
log(`${ts()} R2: ada3 on app -> ${JSON.stringify(await probe(a3, APP))}`);
execFileSync('python3', ['-I', '-c', `
import sys; sys.path.insert(0, "${SPIKE}scripts")
import kanidm_api as k
k.request("DELETE", "/v1/group/adults/_attr/member", token=k.manager_token(), body=["ada3"])
print("removed ada3 from adults")`], { stdio: 'inherit' });
const r2Since = Date.now();
const r2 = await pollUntilDenied('R2 role removed', a3, APP, 10000, 5 * 60 * 1000, r2Since);
await a3.goto(APP); const t2 = await a3.title();
log(`${ts()} R2: ada3 signing in again lands on "${t2}" at ${a3.url().split('?')[0]}`);

// R3: ada on app (SSO in ada's context), then delete the adults scope map of whoami
const adaA = await adaCtx.newPage();
await adaA.goto(APP); await signIn(adaA, 'ada');
await adaA.waitForURL((u) => !u.toString().startsWith(IDM), { timeout: 20000 }).catch(() => {});
log(`${ts()} R3: ada on app -> ${JSON.stringify(await probe(adaA, APP))}`);
log(`${ts()} R3: ` + py('scopemap-del', 'whoami', 'adults'));
const r3Since = Date.now();
const r3 = await pollUntilDenied('R3 scope map removed', adaA, APP, 15000, 4 * 60 * 1000, r3Since);
{
  const ctx = await newCtx(); const p = await ctx.newPage();
  await p.goto(APP); await signIn(p, 'ada');
  log(`${ts()} R3: a fresh sign-in by ada now lands on "${await p.title()}" at ${p.url().split('?')[0]}`);
  await ctx.close();
}
log(`${ts()} R3: restore -> ` + py('scopemap-set', 'whoami', 'adults', 'openid', 'profile', 'email', 'groups_name'));

const r1s = await r1;
log(`\nsummary: R1 client deleted -> denied after ${r1s}s; R2 role removed -> denied after ${r2}s; R3 scope map removed -> ${r3 === null ? 'NOT denied within 240s' : 'denied after ' + r3 + 's'}`);
await browser.close();
