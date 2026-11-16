// The household account's passkeys in a real browser: Chromium's virtual authenticator (the CDP WebAuthn domain), on a
// real Authentik that the identity provider contract suite set up (internal/idp/authentik, compose project hltest-17).
// That suite runs this file and hands it, in HOMEWARD_TEST_SIGNIN, a JSON file: the sign-in's address on this machine,
// a one-time enrolment link, and an authorize URL of Admin's client. Without it, there is nothing to run.
//
// What it checks, as a browser sees it (#83, #84):
//   - the enrolment link makes a passkey whose RP ID is the sign-in's own name, with user verification;
//   - Admin's client signs the admin in with that passkey, and nothing else;
//   - a step-up on the live session asks for the passkey again (max_age), and comes back with a code of its own (#25);
//   - an authenticator that can't verify the person signs nobody in.
//
// The virtual authenticator answers at once, a person never does: passkeys.ts holds its answer until the page's own
// requests to the flow are answered, as a person's touch would come after them (#222).
import { readFileSync, writeFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { asAPersonWould, authenticator } from './passkeys';

type World = {
  address: string; // host:port of the sign-in's TLS stand-in for the edge, on this machine
  spki: string; // its certificate's public key hash, which Chromium takes as trusted
  enrolment: string;
  authorize: string;
  username: string;
  redirect: string;
  landing: string;
  out: string; // where this file writes what the browser came back with
  stepUp: string; // an authorize URL of Admin's client for a step-up, which this browser opens once signed in
  outStepUp: string; // where this file writes what the step-up came back with
};

const file = process.env.HOMEWARD_TEST_SIGNIN;
const world: World | null = file ? (JSON.parse(readFileSync(file, 'utf8')) as World) : null;

test.skip(!world, 'run by the identity provider contract suite (HOMEWARD_TEST_SIGNIN)');
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
// Where Admin's client was sent back, with its code: handed to the suite once both tests are done (it then ends).
let callback = '';

async function signIn(page: Page, w: World, authorize = w.authorize) {
  await page.goto(authorize);
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
  await asAPersonWould(context);
  const page = await context.newPage();
  const { cdp, id } = await authenticator(context, page, true);

  await page.goto(w.enrolment);
  await page.waitForURL((u) => u.href.startsWith(w.landing), { timeout: 60_000 });
  const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId: id });
  expect(credentials).toHaveLength(1);
  const made = credentials[0];
  expect(made?.rpId).toBe('auth.example.test');
  expect(made?.isResidentCredential).toBe(true);
  passkey = (made ?? null) as unknown as Record<string, unknown> | null;
  await context.close();

  // The same passkey, on the person's device next time: a fresh browser with it on its authenticator.
  const later = await browser.newContext();
  await asAPersonWould(later);
  const again = await later.newPage();
  const second = await authenticator(later, again, true);
  await second.cdp.send('WebAuthn.addCredential', { authenticatorId: second.id, credential: passkey as never });
  await signIn(again, w);
  await again.waitForURL((u) => u.href.startsWith(w.redirect), { timeout: 60_000 });
  callback = again.url();
  // As the device keeps it: its signature counter now past this sign-in's, which the sign-in checks next time.
  const { credentials: kept } = await second.cdp.send('WebAuthn.getCredentials', { authenticatorId: second.id });
  passkey = (kept[0] ?? null) as unknown as Record<string, unknown> | null;
  await later.close();
});

// In a browser of its own: a new sign-in replaces the session at the sign-in, and the codes it gave before with it.
test('a step-up on a live session asks for the passkey again, and comes back with its own code (#25)', async ({ browser }) => {
  const w = world as World;
  expect(passkey).not.toBeNull();
  const context = await browser.newContext();
  await asAPersonWould(context);
  const page = await context.newPage();
  const { cdp, id } = await authenticator(context, page, true);
  await cdp.send('WebAuthn.addCredential', { authenticatorId: id, credential: passkey as never });
  await signIn(page, w);
  await page.waitForURL((u) => u.href.startsWith(w.redirect), { timeout: 60_000 });

  // Once the session is older than the step-up's max_age, the sign-in asks for the passkey again, and the browser
  // comes back with the step-up's own state.
  const used = async () => (await cdp.send('WebAuthn.getCredentials', { authenticatorId: id })).credentials[0]?.signCount ?? 0;
  const before = await used();
  await page.waitForTimeout(7_000);
  const state = new URL(w.stepUp).searchParams.get('state');
  await signIn(page, w, w.stepUp);
  await page.waitForURL((u) => u.href.startsWith(w.redirect) && u.searchParams.get('state') === state, { timeout: 60_000 });
  expect(await used()).toBeGreaterThan(before);
  writeFileSync(w.outStepUp, page.url());
  await context.close();
});

test('the same passkey, on an authenticator that doesn’t verify the person (a touch alone), signs nobody in', async ({ browser }) => {
  const w = world as World;
  expect(passkey).not.toBeNull();
  const context = await browser.newContext();
  await asAPersonWould(context);
  const page = await context.newPage();
  const { cdp, id } = await authenticator(context, page, false);
  await cdp.send('WebAuthn.addCredential', { authenticatorId: id, credential: passkey as never });
  await signIn(page, w);
  await page.waitForTimeout(8_000);
  expect(page.url().startsWith(w.redirect)).toBe(false);
  await context.close();
  writeFileSync(w.out, callback);
});
