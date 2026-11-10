// HomersLab spike: Caddy forward_auth -> oauth2-proxy -> Kanidm 1.12, driven in headless Chromium.
// Persons sign in with password + TOTP through Kanidm's own UI. No secret is ever logged.
// Usage: node e2e.mjs            (all phases; the last one deletes the 'whoami' client)
import { chromium } from 'playwright';
import { readFileSync, writeFileSync, appendFileSync } from 'node:fs';
import { createHmac, createHash, randomBytes } from 'node:crypto';
import { execFileSync } from 'node:child_process';

const SPIKE = new URL('..', import.meta.url).pathname;
const APP = 'https://app.hlspike.test:28744';
const IDM = 'https://idm.hlspike.test:28743';
const OUT = `${SPIKE}results/05-e2e.txt`;
const JSON_OUT = `${SPIKE}results/05-e2e.json`;
writeFileSync(OUT, '');
const results = [];
const t0 = Date.now();
const ts = () => `[+${((Date.now() - t0) / 1000).toFixed(1)}s]`;
const redact = (s) => String(s).replace(/(code_challenge|code|state|nonce|token)=[^&\s]+/g, '$1=..');
function log(...a) { const s = a.join(' '); console.log(s); appendFileSync(OUT, s + '\n'); }
function check(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail });
  log(`${ts()} ${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '\n        ' + detail : ''}`);
}

// ---- credentials and TOTP (never printed) ----
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
  if (lastStep[person] !== undefined && step <= lastStep[person]) {   // Kanidm refuses a reused code
    await new Promise((r) => setTimeout(r, ((lastStep[person] + 1) * c.step * 1000) - Date.now() + 1500));
    step = Math.floor(Date.now() / 1000 / c.step);
  }
  lastStep[person] = step;
  const ctr = Buffer.alloc(8); ctr.writeBigUInt64BE(BigInt(step));
  const h = createHmac(c.algo, b32decode(c.totp_secret_b32)).update(ctr).digest();
  const o = h[h.length - 1] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 10 ** c.digits).padStart(c.digits, '0');
}
const py = (script, ...args) => execFileSync('python3', ['-I', `${SPIKE}scripts/${script}`, ...args],
  { encoding: 'utf8' }).trim();
const b64url = (b) => Buffer.from(b).toString('base64url');

// ---- browser helpers ----
async function visibleFields(page) {
  const out = [];
  for (const sel of ['#username', '#totp', '#password']) {
    const el = page.locator(sel);
    if (await el.count() && await el.isVisible()) out.push(sel.slice(1));
  }
  return out;
}
// Sign in through Kanidm's UI: whatever field is visible (username, then TOTP, then password).
async function kanidmSignIn(page, person) {
  const asked = [];
  for (let i = 0; i < 6; i++) {
    if (!page.url().startsWith(IDM)) break;
    const fields = await visibleFields(page);
    if (!fields.length) break;
    const f = fields[0];
    const val = f === 'username' ? person : f === 'totp' ? await freshTotp(person) : creds(person).password;
    try { await page.locator('#' + f).fill(val, { timeout: 5000 }); } catch { throw new Error(`fill ${f} failed`); }
    asked.push(f);
    await Promise.all([page.waitForNavigation({ timeout: 20000 }).catch(() => {}),
      page.locator('button[type="submit"]:visible').first().click()]);
  }
  return asked;
}
async function getApp(page, path = '/api') {
  const resp = await page.goto(APP + path);
  const atApp = page.url().startsWith(APP + path);
  let json = null;
  if (atApp && resp.status() === 200) { try { json = JSON.parse(await resp.text()); } catch { /* html */ } }
  return { status: resp.status(), url: page.url(), atApp, json };
}
const pick = (h, names) => Object.fromEntries(names.filter((n) => h[n]).map((n) => [n, h[n].join(',')]));
const ID_HEADERS = ['X-Auth-Request-User', 'X-Auth-Request-Email', 'X-Auth-Request-Preferred-Username',
  'X-Auth-Request-Groups'];

const browser = await chromium.launch({ args: ['--host-resolver-rules=MAP *.hlspike.test 127.0.0.1'] });
const newCtx = () => browser.newContext({ ignoreHTTPSErrors: true });
const UNTIL = Number(process.env.UNTIL || 99);
async function stopAfter(n) {
  if (UNTIL > n) return;
  await browser.close();
  const failed = results.filter((r) => !r.ok);
  log(`\n(stopped after phase ${n}) ${results.length - failed.length}/${results.length} checks passed`);
  process.exit(failed.length ? 1 : 0);
}

// ===== 1. unauthenticated =====
log('== 1. unauthenticated');
{
  const ctx = await newCtx(); const page = await ctx.newPage();
  const resp = await page.goto(APP + '/api');
  const chain = []; for (let r = resp.request(); r; r = r.redirectedFrom()) chain.unshift(new URL(r.url()).origin + new URL(r.url()).pathname);
  check('unauthenticated request is redirected to sign-in', page.url().startsWith(IDM) &&
    chain.some((u) => u.endsWith('/oauth2/sign_in')), 'chain: ' + chain.join(' -> ') + ` | fields: ${await visibleFields(page)}`);
  await ctx.setExtraHTTPHeaders({ 'X-Auth-Request-User': 'forged-admin', 'X-Auth-Request-Groups': 'adults',
    'X-Forwarded-User': 'forged', Authorization: 'Bearer forged' });
  const r2 = await getApp(page);
  check('forged identity headers without a session do not get in', !r2.atApp, `ended at ${redact(r2.url).split('?')[0]}`);
  await ctx.close();
}

await stopAfter(1);
// ===== 2. the adult =====
log('== 2. adult (ada, in adults) signs in with password + TOTP');
const adultCtx = await newCtx(); const adult = await adultCtx.newPage();
await adult.goto(APP + '/api');
const asked = await kanidmSignIn(adult, 'ada');
const tLogin = Math.floor(Date.now() / 1000);
await adult.waitForURL((u) => u.toString().startsWith(APP), { timeout: 20000 }).catch(() => {});
const a1 = await getApp(adult);
check('adult gets 200 from the guarded app', a1.status === 200 && a1.atApp, `Kanidm asked for: ${asked.join(' -> ')}`);
const realIds = a1.json ? pick(a1.json.headers, ID_HEADERS) : {};
check('identity headers reach the app', realIds['X-Auth-Request-Email'] === 'ada@hlspike.test' &&
  realIds['X-Auth-Request-Preferred-Username'] === 'ada' && (realIds['X-Auth-Request-Groups'] || '').split(',').includes('adults'),
  JSON.stringify(realIds));
const cookies = (await adultCtx.cookies(APP)).map((c) => `${c.name} (secure=${c.secure}, httpOnly=${c.httpOnly}, sameSite=${c.sameSite})`);
log(`        cookies on the app: ${cookies.join('; ')}`);

await stopAfter(2);
// ===== 3. the kid =====
log('== 3. kid (kim, in kids) signs in');
const kidCtx = await newCtx(); const kid = await kidCtx.newPage();
await kid.goto(APP + '/api');
const kidAsked = await kanidmSignIn(kid, 'kim');
const kidTitle = await kid.title();
const kidBody = (await kid.locator('body').innerText()).replace(/\s+/g, ' ').slice(0, 160);
check('kid is refused (Kanidm: no scope map for kids)', kid.url().startsWith(IDM) && /denied/i.test(kidTitle + kidBody),
  `asked: ${kidAsked.join(' -> ')} | page "${kidTitle}" at ${redact(kid.url()).split('?')[0]}: ${kidBody}`);
const k2 = await getApp(kid);
check('kid still cannot reach the app afterwards', !k2.atApp, `ended at ${redact(k2.url).split('?')[0]}`);

await stopAfter(3);
// ===== 4. forged headers from an authenticated client =====
log('== 4. forged identity headers from the signed-in adult');
await adultCtx.setExtraHTTPHeaders({
  'X-Auth-Request-User': 'forged-admin', 'X-Auth-Request-Email': 'evil@example.com',
  'X-Auth-Request-Groups': 'admins', 'X-Auth-Request-Preferred-Username': 'root',
  'X-Auth-Request-Access-Token': 'forged-token', 'Remote-User': 'forged-remote',
  'X-Forwarded-User': 'forged-xfu', 'X-Spike-Unlisted': 'forged-unlisted' });
const f1 = await getApp(adult);
const fh = f1.json ? f1.json.headers : {};
const seen = pick(fh, [...ID_HEADERS, 'X-Auth-Request-Access-Token', 'Remote-User', 'X-Forwarded-User', 'X-Spike-Unlisted']);
check('copied identity headers carry the real identity, not the forged values',
  f1.status === 200 && ID_HEADERS.every((h) => seen[h] === realIds[h]), JSON.stringify(seen));
check('a copied header that oauth2-proxy did not return is deleted (Caddy >= 2.11.2)', !fh['X-Auth-Request-Access-Token']);
check('headers the edge strips explicitly (Remote-User, X-Forwarded-User) are gone', !fh['Remote-User'] && !fh['X-Forwarded-User']);
log(`        note: a header neither copied nor stripped passes through: X-Spike-Unlisted=${seen['X-Spike-Unlisted'] || '(absent)'}`);
await adultCtx.setExtraHTTPHeaders({});

await stopAfter(4);
// ===== 5. step-up: prompt=login and max_age against a probe client =====
log('== 5. step-up (prompt / max_age) with the adult\'s live Kanidm session');
async function authorize(page, person, params, { complete = true } = {}) {
  const verifier = b64url(randomBytes(32));
  const challenge = b64url(createHash('sha256').update(verifier).digest());
  const state = b64url(randomBytes(12));
  const nonce = params.nonce || b64url(randomBytes(12));
  const q = new URLSearchParams({ response_type: 'code', client_id: 'stepup-probe', redirect_uri: APP + '/stepup/callback',
    scope: 'openid profile groups_name', state, code_challenge: challenge, code_challenge_method: 'S256', ...params, nonce });
  await page.goto(`${IDM}/ui/oauth2?${q}`);
  const prompted = page.url().startsWith(IDM) ? await visibleFields(page) : [];
  let asked = [];
  if (prompted.length && complete) asked = await kanidmSignIn(page, person);
  await page.waitForURL((u) => !u.toString().startsWith(IDM), { timeout: 10000 }).catch(() => {});
  const u = new URL(page.url());
  const res = { prompted, asked, landed: u.origin + u.pathname };
  if (!u.toString().startsWith(APP + '/stepup/callback')) return res;
  if (u.searchParams.get('error')) return { ...res, error: u.searchParams.get('error') };
  res.stateOk = u.searchParams.get('state') === state;
  res.claims = JSON.parse(py('token_exchange.py', 'stepup-probe', 'stepup-probe.secret', APP + '/stepup/callback',
    u.searchParams.get('code'), verifier));
  return res;
}
const show = (r) => JSON.stringify({ prompted: r.prompted, asked: r.asked, landed: r.landed, error: r.error,
  auth_time: r.claims?.auth_time, nonce: r.claims?.nonce, amr: r.claims?.amr, acr: r.claims?.acr });
log(`        adult's sign-in at unix ${tLogin}`);
const s0 = await authorize(adult, 'ada', {});
check('no prompt: code issued silently (SSO)', !s0.prompted.length && s0.claims, show(s0));
log(`        ID token: ${JSON.stringify(s0.claims)}`);
const s1 = await authorize(adult, 'ada', { max_age: '3600' });
check('max_age=3600: no prompt, session is fresh enough', !s1.prompted.length && s1.claims, show(s1));
await new Promise((r) => setTimeout(r, 2500));
const plan = 'plan-' + b64url(createHash('sha256').update('install whoami, public').digest()).slice(0, 16);
const s2 = await authorize(adult, 'ada', { prompt: 'login', nonce: plan });
check('prompt=login: Kanidm asks again, then auth_time is fresh', s2.prompted.length && s2.claims &&
  s2.claims.auth_time > s0.claims.auth_time, show(s2));
check('the step-up is bound to the plan through the nonce', s2.claims?.nonce === plan && s2.stateOk, `nonce=${s2.claims?.nonce}`);
await new Promise((r) => setTimeout(r, 2500));
const s3 = await authorize(adult, 'ada', { max_age: '0' });
check('max_age=0: Kanidm asks again, then auth_time is fresh', s3.prompted.length && s3.claims &&
  s3.claims.auth_time > s2.claims.auth_time, show(s3));
const s4 = await authorize(adult, 'ada', { prompt: 'none' });
check('prompt=none with a session: code issued silently', !s4.prompted.length && s4.claims, show(s4));
{
  const ctx = await newCtx(); const p = await ctx.newPage();
  const s5 = await authorize(p, 'ada', { prompt: 'none' });
  check('prompt=none without a session: error=login_required, no UI', s5.error === 'login_required', show(s5));
  const s6 = await authorize(p, 'ada', { prompt: 'login' }, { complete: false });
  log(`        prompt=login without a session shows: ${JSON.stringify(s6.prompted)}`);
  await ctx.close();
}
{
  const ctx = await newCtx(); const p = await ctx.newPage();
  await p.goto(APP + '/api'); await kanidmSignIn(p, 'kim');   // kid, not in the probe's scope map
  const s7 = await authorize(p, 'kim', { prompt: 'login' }, { complete: false });
  log(`        kid on the probe client: ${show(s7)} title="${await p.title()}"`);
  await ctx.close();
}

await stopAfter(5);
// ===== 6. a per-route audience on the shared guard (allowed_groups query) =====
log('== 6. /kids-only/ uses /oauth2/auth?allowed_groups=kids');
const ko = await getApp(adult, '/kids-only/api');
check('adult is refused on a kids-only route (403 from oauth2-proxy, passed on by Caddy)', ko.status === 403, `status ${ko.status} at ${ko.url}`);

await stopAfter(6);
// ===== 7. audience at the guard: Kanidm lets the kid through, oauth2-proxy's allowed group refuses =====
log('== 7. scope map widened to kids by API; oauth2-proxy --allowed-group=adults must still refuse the kid');
log('        ' + py('admin.py', 'scopemap-set', 'whoami', 'kids', 'openid', 'profile', 'email', 'groups_name'));
const k3 = await getApp(kid);
const k3body = (await kid.locator('body').innerText().catch(() => '')).replace(/\s+/g, ' ').slice(0, 120);
check('kid passes Kanidm but is refused by oauth2-proxy (allowed group)', !k3.atApp && k3.status === 403,
  `status ${k3.status} at ${redact(k3.url).split('?')[0]}: ${k3body}`);
log('        ' + py('admin.py', 'scopemap-del', 'whoami', 'kids'));

await stopAfter(7);
// ===== 8. removing a person revokes their guard session =====
log('== 8. person removal');
log(py('admin.py', 'person-add', 'ada2', 'adults').split('\n').map((l) => '        ' + l).join('\n'));
const a2Ctx = await newCtx(); const a2 = await a2Ctx.newPage();
await a2.goto(APP + '/api'); await kanidmSignIn(a2, 'ada2');
await a2.waitForURL((u) => u.toString().startsWith(APP), { timeout: 20000 }).catch(() => {});
const a2ok = await getApp(a2);
check('second adult (ada2) gets 200', a2ok.status === 200 && a2ok.atApp);
log('        ' + py('admin.py', 'person-delete', 'ada2'));
const tDel = Date.now();
let revokedAfter = null;
for (let i = 0; i < 24; i++) {
  const r = await getApp(a2);
  if (!r.atApp) { revokedAfter = (Date.now() - tDel) / 1000; log(`        ${ts()} denied, now at ${redact(r.url).split('?')[0]}`); break; }
  log(`        ${ts()} still 200 (${((Date.now() - tDel) / 1000).toFixed(0)}s after deletion)`);
  await new Promise((res) => setTimeout(res, 10000));
}
check('deleted person loses access to the guarded app', revokedAfter !== null, `after ${revokedAfter}s (cookie-refresh 1m)`);
await a2Ctx.close();

await stopAfter(8);
// ===== 9. deleting the client revokes access =====
log('== 9. client deletion');
const pre = await getApp(adult);
check('adult still has access before the client is deleted', pre.status === 200 && pre.atApp);
log(py('admin.py', 'client-delete', 'whoami').split('\n').map((l) => '        ' + l).join('\n'));
const tC = Date.now();
let clientRevokedAfter = null;
for (let i = 0; i < 24; i++) {
  const r = await getApp(adult);
  if (!r.atApp) {
    clientRevokedAfter = (Date.now() - tC) / 1000;
    const body = (await adult.locator('body').innerText().catch(() => '')).replace(/\s+/g, ' ').slice(0, 160);
    log(`        ${ts()} denied, now at ${redact(r.url).split('?')[0]} (HTTP ${r.status}): ${body}`);
    break;
  }
  log(`        ${ts()} still 200 (${((Date.now() - tC) / 1000).toFixed(0)}s after deletion)`);
  await new Promise((res) => setTimeout(res, 10000));
}
check('deleting the client in Kanidm revokes the existing guard session', clientRevokedAfter !== null,
  `after ${clientRevokedAfter}s (cookie-refresh 1m)`);
{
  const ctx = await newCtx(); const p = await ctx.newPage();
  const r = await getApp(p);
  const body = (await p.locator('body').innerText().catch(() => '')).replace(/\s+/g, ' ').slice(0, 160);
  check('a new sign-in for the deleted client fails', !r.atApp, `HTTP ${r.status} at ${redact(r.url).split('?')[0]}: ${body}`);
  await ctx.close();
}

await browser.close();
const failed = results.filter((r) => !r.ok);
log(`\n${results.length - failed.length}/${results.length} checks passed${failed.length ? '; FAILED: ' + failed.map((f) => f.name).join(' | ') : ''}`);
writeFileSync(JSON_OUT, JSON.stringify({ when: new Date().toISOString(), results }, null, 2));
process.exit(failed.length ? 1 : 0);
