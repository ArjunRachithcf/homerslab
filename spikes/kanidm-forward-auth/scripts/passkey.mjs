// Passkey person + step-up as a fresh passkey assertion.
// 1. The manager issues a credential-update intent for 'pat' (an adult) by API.
// 2. In Chromium (virtual authenticator over CDP; Kanidm's key pinned with --ignore-certificate-errors-spki-list),
//    the intent is exchanged and a passkey enrolled through Kanidm's REST API, same-origin.
// 3. pat signs in to the step-up probe client with the passkey; then prompt=login and max_age=0 must cause a
//    fresh assertion (the authenticator's signCount rises) and a newer auth_time.
import { chromium } from 'playwright';
import { readFileSync, writeFileSync, appendFileSync } from 'node:fs';
import { createHash, randomBytes } from 'node:crypto';
import { execFileSync } from 'node:child_process';

const SPIKE = new URL('..', import.meta.url).pathname;
const IDM = 'https://idm.hlspike.test:28743';
const CB = 'https://app.hlspike.test:28744/stepup/callback';
const OUT = `${SPIKE}results/12-passkey.txt`;
writeFileSync(OUT, '');
const log = (...a) => { const s = a.join(' '); console.log(s); appendFileSync(OUT, s + '\n'); };
const b64url = (b) => Buffer.from(b).toString('base64url');

// 1. person + intent (manager token)
execFileSync('python3', ['-I', '-c', `
import sys, os; sys.path.insert(0, "${SPIKE}scripts")
import kanidm_api as k, setup
setup.ensure_person("pat", "Pat (adult, passkey)", "pat@hlspike.test")
setup.call("POST", "/v1/group/adults/_attr/member", ["pat"])
st, intent, _ = k.request("GET", "/v1/person/pat/_credential/_update_intent/600", token=k.manager_token())
k.save_secret("pat.intent", intent["token"])
print("  intent for pat issued by the manager")`], { stdio: 'inherit' });
const intent = readFileSync(`${SPIKE}secrets/pat.intent`, 'utf8').trim();

const spki = readFileSync(`${SPIKE}results/.spki-list.txt`, 'utf8').trim();
const browser = await chromium.launch({ args: ['--host-resolver-rules=MAP *.hlspike.test 127.0.0.1',
  `--ignore-certificate-errors-spki-list=${spki}`] });
const ctx = await browser.newContext();
const page = await ctx.newPage();
await page.route(CB + '**', (route) => route.fulfill({ status: 200, contentType: 'text/plain', body: 'callback' }));
const cdp = await ctx.newCDPSession(page);
await cdp.send('WebAuthn.enable');
const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', { options: {
  protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true } });
const signCount = async () => (await cdp.send('WebAuthn.getCredentials', { authenticatorId })).credentials.map((c) => c.signCount);

// 2. enrol the passkey through the REST credential-update session, same-origin from a Kanidm page
await page.goto(IDM + '/ui/login');
const enrol = await page.evaluate(async (intentToken) => {
  const post = async (path, body) => {
    const r = await fetch(path, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    if (!r.ok) throw new Error(`${path} -> ${r.status} ${(await r.text()).slice(0, 200)}`);
    return r.json();
  };
  const fromB64 = (s) => Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((s.length + 3) % 4)), (c) => c.charCodeAt(0));
  const toB64 = (b) => btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  const [session, st0] = await post('/v1/credential/_exchange_intent', intentToken);
  const st1 = await post('/v1/credential/_update', ['passkeyinit', session]);
  const opts = st1.mfaregstate.Passkey.publicKey;
  const pk = { ...opts, challenge: fromB64(opts.challenge), user: { ...opts.user, id: fromB64(opts.user.id) },
    excludeCredentials: (opts.excludeCredentials || []).map((c) => ({ ...c, id: fromB64(c.id) })) };
  const cred = await navigator.credentials.create({ publicKey: pk });
  const reg = { id: cred.id, rawId: toB64(cred.rawId), type: cred.type, extensions: {},
    response: { attestationObject: toB64(cred.response.attestationObject), clientDataJSON: toB64(cred.response.clientDataJSON) } };
  const st2 = await post('/v1/credential/_update', [{ passkeyfinish: ['pat-virtual-passkey', reg] }, session]);
  await post('/v1/credential/_commit', session);
  return { before: st0.warnings, rp: opts.rp, uv: opts.authenticatorSelection?.userVerification,
    passkeys: st2.passkeys.map((p) => p.tag), can_commit: st2.can_commit, warnings: st2.warnings };
}, intent);
log('enrolment:', JSON.stringify(enrol), '| signCount', JSON.stringify(await signCount()));

// 3. sign in with the passkey, then step up
async function authorize(params) {
  const verifier = b64url(randomBytes(32));
  const q = new URLSearchParams({ response_type: 'code', client_id: 'stepup-probe', redirect_uri: CB,
    scope: 'openid profile groups_name', state: b64url(randomBytes(8)), nonce: b64url(randomBytes(8)),
    code_challenge: b64url(createHash('sha256').update(verifier).digest()), code_challenge_method: 'S256', ...params });
  const before = await signCount();
  await page.goto(`${IDM}/ui/oauth2?${q}`);
  const seen = [];
  for (let i = 0; i < 6 && page.url().startsWith(IDM); i++) {
    const user = page.locator('#username');
    if (await user.count() && await user.isVisible()) {
      await user.fill('pat'); seen.push('username');
      await Promise.all([page.waitForNavigation({ timeout: 15000 }).catch(() => {}), page.locator('button[type="submit"]:visible').first().click()]);
      continue;
    }
    const btn = page.locator('button:visible', { hasText: /passkey|security key|webauthn|authenticate|continue|use/i }).first();
    if (await btn.count()) {
      seen.push('button:' + (await btn.innerText()).trim().slice(0, 30));
      await Promise.all([page.waitForNavigation({ timeout: 15000 }).catch(() => {}), btn.click()]);
      continue;
    }
    seen.push('waiting'); await page.waitForTimeout(1500);
  }
  const after = await signCount();
  const u = new URL(page.url());
  if (!page.url().startsWith(CB)) {
    return { seen, before, after, landed: u.origin + u.pathname, title: await page.title(),
      body: (await page.locator('body').innerText().catch(() => '')).replace(/\s+/g, ' ').slice(0, 200) };
  }
  const claims = JSON.parse(execFileSync('python3', ['-I', `${SPIKE}scripts/token_exchange.py`, 'stepup-probe',
    'stepup-probe.secret', CB, u.searchParams.get('code'), verifier], { encoding: 'utf8' }));
  return { seen, before, after, auth_time: claims.auth_time, amr: claims.amr, sub: claims.sub, user: claims.preferred_username };
}
const first = await authorize({});
log('first sign-in (passkey):', JSON.stringify(first));
await page.waitForTimeout(2000);
const silent = await authorize({});
log('again without prompt (SSO):', JSON.stringify(silent));
await page.waitForTimeout(2000);
const login = await authorize({ prompt: 'login' });
log('prompt=login:', JSON.stringify(login));
await page.waitForTimeout(2000);
const zero = await authorize({ max_age: '0' });
log('max_age=0:', JSON.stringify(zero));
const fresh = (r, prev) => r.auth_time > prev.auth_time && r.after[0] === r.before[0] + 1;
log(`\nRESULT passkey sign-in works: ${!!first.auth_time}; SSO without prompt makes no assertion: ${silent.after[0] === silent.before[0]};`,
  `prompt=login = fresh passkey assertion: ${fresh(login, silent)}; max_age=0 = fresh passkey assertion: ${fresh(zero, login)}`);
await browser.close();
