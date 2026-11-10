// A/B: does a guard session survive oauth2-proxy's cookie refresh behind Caddy?
//  A: /api        plain forward_auth (the refreshed Set-Cookie on the 202 never reaches the browser)
//  B: /fixed/api  forward_auth written out, with copy_response_headers { include Set-Cookie } on 2xx
// Each poll records whether the request bounced through sign-in/Kanidm and whether the cookie changed.
import { chromium } from 'playwright';
import { readFileSync, writeFileSync, appendFileSync } from 'node:fs';
import { createHmac, createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';

const SPIKE = new URL('..', import.meta.url).pathname;
const IDM = 'https://idm.hlspike.test:28743';
const APP = 'https://app.hlspike.test:28744';
const OUT = `${SPIKE}results/16-scope-map-fixed-route.txt`;
writeFileSync(OUT, '');
const t0 = Date.now();
const ts = () => `[${new Date().toISOString().slice(11, 19)} +${((Date.now() - t0) / 1000).toFixed(0)}s]`;
const log = (...a) => { const s = a.join(' '); console.log(s); appendFileSync(OUT, s + '\n'); };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const creds = (p) => JSON.parse(readFileSync(`${SPIKE}secrets/${p}.json`, 'utf8'));
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
const cookieTag = async (ctx) => {
  const c = (await ctx.cookies(APP)).find((x) => x.name === '_hl_guard');
  return c ? createHash('sha256').update(c.value).digest('hex').slice(0, 8) : '(none)';
};

const browser = await chromium.launch({ args: ['--host-resolver-rules=MAP *.hlspike.test 127.0.0.1'] });
async function session(label, path) {
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
  const page = await ctx.newPage();
  await page.goto(APP + path); await signIn(page, 'ada');
  await page.waitForURL((u) => u.toString().startsWith(APP + path), { timeout: 20000 }).catch(() => {});
  log(`${ts()} ${label}: signed in, at ${page.url()} cookie=${await cookieTag(ctx)}`);
  return { label, path, ctx, page, bounces: 0, cookies: new Set([await cookieTag(ctx)]), fails: 0 };
}
async function poll(s) {
  const before = await cookieTag(s.ctx);
  const resp = await s.page.goto(APP + s.path);
  const chain = []; for (let r = resp.request(); r; r = r.redirectedFrom()) chain.unshift(new URL(r.url()).host + new URL(r.url()).pathname);
  const bounced = chain.length > 1;
  const ok = s.page.url() === APP + s.path && resp.status() === 200;
  const after = await cookieTag(s.ctx);
  if (bounced) s.bounces++;
  if (!ok) s.fails++;
  s.cookies.add(after);
  log(`${ts()} ${s.label}: HTTP ${resp.status()} ${ok ? 'ok' : 'NOT ok'}${bounced ? ' BOUNCED via ' + chain.slice(1, -1).join(' > ') : ''} cookie ${before}->${after}`);
}


const admin = (...a) => execFileSync('python3', ['-I', `${SPIKE}scripts/admin.py`, ...a], { encoding: 'utf8' }).trim();
// R3 again, on the fixed route: the refreshed cookie now reaches the browser, so only Kanidm decides.
const S = await session('R3-fixed', '/fixed/api');
log(`${ts()} ` + admin('scopemap-del', 'whoami', 'adults'));
for (let i = 0; i < 8; i++) { await sleep(20000); await poll(S); }
{
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true }); const p = await ctx.newPage();
  await p.goto(APP + '/fixed/api'); await signIn(p, 'ada');
  log(`${ts()} a fresh sign-in by ada now lands on "${await p.title()}" at ${p.url().split('?')[0]}`);
  await ctx.close();
}
log(`${ts()} restore: ` + admin('scopemap-set', 'whoami', 'adults', 'openid', 'profile', 'email', 'groups_name'));
log(`summary R3-fixed: ${S.bounces} bounce(s) through sign-in, ${S.fails} failed poll(s), ${S.cookies.size} distinct cookie values over ${8 * 20}s after the scope map was removed`);
await browser.close();
