// The household account's passkeys in a real browser: Chromium's virtual authenticator (the CDP WebAuthn domain), on a
// real Authentik that the identity provider contract suite set up (internal/idp/authentik, compose project hltest-17).
// That suite runs this file and hands it, in HOMERSLAB_TEST_SIGNIN, a JSON file: the sign-in's address on this machine,
// a one-time enrolment link, and an authorize URL of Admin's client. Without it, there is nothing to run.
//
// What it checks, as a browser sees it (#83, #84):
//   - the enrolment link makes a passkey whose RP ID is the sign-in's own name, with user verification;
//   - Admin's client signs the admin in with that passkey, and nothing else;
//   - an authenticator that can't verify the person signs nobody in.
import { readFileSync, writeFileSync } from 'node:fs';
import { expect, test, type BrowserContext, type CDPSession, type Page } from '@playwright/test';

type World = {
  address: string; // host:port of the sign-in's TLS stand-in for the edge, on this machine
  spki: string; // its certificate's public key hash, which Chromium takes as trusted
  enrolment: string;
  authorize: string;
  username: string;
  redirect: string;
  landing: string;
  out: string; // where this file writes what the browser came back with
};

const file = process.env.HOMERSLAB_TEST_SIGNIN;
const world: World | null = file ? (JSON.parse(readFileSync(file, 'utf8')) as World) : null;

test.skip(!world, 'run by the identity provider contract suite (HOMERSLAB_TEST_SIGNIN)');
test.describe.configure({ mode: 'serial', timeout: 180_000 });

test.use({
  ignoreHTTPSErrors: false,
  launchOptions: world
    ? { args: [`--host-resolver-rules=MAP auth.example.test:443 ${world.address}, MAP admin.example.test:443 ${world.address}`,
        `--ignore-certificate-errors-spki-list=${world.spki}`] }
    : {},
});

// The admin's passkey, as the first test made it: the second puts it on an authenticator that doesn't verify the person.
let passkey: Record<string, unknown> | null = null;

async function authenticator(context: BrowserContext, page: Page, verifies: boolean): Promise<{ cdp: CDPSession; id: string }> {
  const cdp = await context.newCDPSession(page);
  await cdp.send('WebAuthn.enable', { enableUI: false });
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true,
      isUserVerified: verifies, automaticPresenceSimulation: true },
  });
  return { cdp, id: authenticatorId };
}

async function signIn(page: Page, w: World) {
  await page.goto(w.authorize);
  // Authentik also keeps hidden username and password fields, off the screen, for password managers.
  const user = page.getByRole('textbox', { name: 'Email or Username' });
  await user.waitFor({ timeout: 30_000 });
  await expect(page.getByRole('textbox', { name: /password/i })).toHaveCount(0); // the passkey-only flow asks for no password
  await user.fill(w.username);
  await page.getByRole('button', { name: 'Log in' }).click();
}

test('the enrolment link makes a passkey for the sign-in’s own name, and Admin’s client signs in with it', async ({ browser }) => {
  const w = world as World;
  const context = await browser.newContext();
  const page = await context.newPage();
  const { cdp, id } = await authenticator(context, page, true);

  await page.goto(w.enrolment);
  await page.waitForURL((u) => u.href.startsWith(w.landing), { timeout: 60_000 });
  const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId: id });
  expect(credentials).toHaveLength(1);
  expect(credentials[0].rpId).toBe('auth.example.test');
  expect(credentials[0].isResidentCredential).toBe(true);
  passkey = credentials[0] as unknown as Record<string, unknown>;
  await context.close();

  // The same passkey, on the person's device next time: a fresh browser with it on its authenticator.
  const later = await browser.newContext();
  const again = await later.newPage();
  const second = await authenticator(later, again, true);
  await second.cdp.send('WebAuthn.addCredential', { authenticatorId: second.id, credential: passkey as never });
  await signIn(again, w);
  await again.waitForURL((u) => u.href.startsWith(w.redirect), { timeout: 60_000 });
  writeFileSync(w.out, again.url());
  await later.close();
});

test('the same passkey, on an authenticator that doesn’t verify the person (a touch alone), signs nobody in', async ({ browser }) => {
  const w = world as World;
  expect(passkey).not.toBeNull();
  const context = await browser.newContext();
  const page = await context.newPage();
  const { cdp, id } = await authenticator(context, page, false);
  await cdp.send('WebAuthn.addCredential', { authenticatorId: id, credential: passkey as never });
  await signIn(page, w);
  await page.waitForTimeout(8_000);
  expect(page.url().startsWith(w.redirect)).toBe(false);
  await context.close();
});
