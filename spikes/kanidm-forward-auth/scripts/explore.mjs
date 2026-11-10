// Exploratory: walk the sign-in once and print the forms Kanidm 1.12 shows (no secrets printed).
import { chromium } from 'playwright';
import { readFileSync } from 'node:fs';
import { createHmac } from 'node:crypto';

const SPIKE = new URL('..', import.meta.url).pathname;
const person = process.argv[2] || 'ada';
const cred = JSON.parse(readFileSync(`${SPIKE}secrets/${person}.json`, 'utf8'));

function b32decode(s) {
  const a = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'; let bits = '';
  for (const c of s.replace(/=+$/, '')) bits += a.indexOf(c).toString(2).padStart(5, '0');
  const out = []; for (let i = 0; i + 8 <= bits.length; i += 8) out.push(parseInt(bits.slice(i, i + 8), 2));
  return Buffer.from(out);
}
function totp() {
  const c = Buffer.alloc(8); c.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 1000 / cred.step)));
  const h = createHmac(cred.algo, b32decode(cred.totp_secret_b32)).update(c).digest();
  const o = h[h.length - 1] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 10 ** cred.digits).padStart(cred.digits, '0');
}

async function dump(page, label) {
  const forms = await page.evaluate(() => [...document.querySelectorAll('input,button,select')].map(e =>
    `${e.tagName.toLowerCase()} type=${e.type || ''} name=${e.name || ''} id=${e.id || ''} value=${e.type === 'password' ? '' : (e.value || '').slice(0, 30)} text=${(e.innerText || '').trim().slice(0, 30)}`));
  console.log(`== ${label}: ${page.url().replace(/(code_challenge|state|nonce|token)=[^&]+/g, '$1=..')}`);
  console.log('   title:', await page.title());
  for (const f of forms) console.log('   ', f);
}

const browser = await chromium.launch({ args: ['--host-resolver-rules=MAP *.hlspike.test 127.0.0.1'] });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await ctx.newPage();
await page.goto('https://app.hlspike.test:28744/');
await dump(page, 'after goto app');
for (let step = 0; step < 6; step++) {
  if (page.url().startsWith('https://app.hlspike.test')) break;
  let filled = '';
  for (const [sel, val] of [['#username', person], ['#totp', null], ['#password', cred.password]]) {
    const el = page.locator(sel);
    if (await el.count() && await el.isVisible()) {
      try { await el.fill(val === null ? totp() : val, { timeout: 5000 }); } catch { throw new Error(`fill ${sel} failed`); }
      filled = sel; break;
    }
  }
  console.log('   filled', filled || '(nothing visible)');
  const btn = page.locator('button[type="submit"]:visible').first();
  await Promise.all([page.waitForLoadState('networkidle').catch(() => {}), btn.click()]);
  await page.waitForTimeout(500);
  await dump(page, `after step ${step}`);
}
console.log('final body:', (await page.locator('body').innerText()).slice(0, 400).replace(/\n+/g, ' | '));
await browser.close();
