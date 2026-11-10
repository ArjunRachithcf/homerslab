// Feasibility: does Chromium allow WebAuthn on Kanidm's origin when its server key is pinned with
// --ignore-certificate-errors-spki-list (no system trust store change)? Uses a CDP virtual authenticator.
import { chromium } from 'playwright';
import { readFileSync } from 'node:fs';

const SPIKE = new URL('..', import.meta.url).pathname;
const spki = readFileSync(`${SPIKE}results/.kanidm-spki.txt`, 'utf8').trim();
const browser = await chromium.launch({ args: ['--host-resolver-rules=MAP *.hlspike.test 127.0.0.1',
  `--ignore-certificate-errors-spki-list=${spki}`] });
const ctx = await browser.newContext();          // note: no ignoreHTTPSErrors here
const page = await ctx.newPage();
const cdp = await ctx.newCDPSession(page);
await cdp.send('WebAuthn.enable');
const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', { options: {
  protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true } });
const resp = await page.goto('https://idm.hlspike.test:28743/ui/login');
console.log('Kanidm page loaded with HTTP', resp.status(), '| secure context:', await page.evaluate(() => isSecureContext));
const r = await page.evaluate(async () => {
  try {
    const c = await navigator.credentials.create({ publicKey: {
      rp: { id: 'idm.hlspike.test', name: 'probe' }, user: { id: new Uint8Array([1, 2, 3]), name: 'probe', displayName: 'probe' },
      challenge: crypto.getRandomValues(new Uint8Array(32)), pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
      authenticatorSelection: { residentKey: 'required', userVerification: 'required' } } });
    return 'created credential of type ' + c.type;
  } catch (e) { return `${e.name}: ${e.message}`; }
});
console.log('navigator.credentials.create ->', r);
const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId });
console.log('virtual authenticator now holds', credentials.length, 'credential(s)');
await browser.close();
