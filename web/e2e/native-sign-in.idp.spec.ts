// An app with native sign-in, opened by a person already signed in to the household account: no second login (#20,
// stories 27 and 32). Chromium's virtual authenticator holds the person's passkey; the app is the official catalog's
// Mealie, installed as its plan says, whose server reaches the sign-in only through the node's sign-in listener (#19).
//
// The identity provider contract suite (internal/idp/authentik, native_test.go) runs this file and hands it, in
// HOMERSLAB_TEST_NATIVE, a JSON file: the real edge's address (it serves both the sign-in and the app, and Mealie's
// server reaches the sign-in through its sign-in listener), a one-time enrolment link, an authorize URL of Admin's
// client (the household account's sign-in, somewhere else first), and the app's URL. Without it there is nothing to run.
//
// What it checks, as a browser sees it:
//   - signed in once, at Admin, the person opens the app and lands in it, signed in as themselves, with no passkey
//     asked again and no sign-in page shown;
//   - the app has no password sign-in of its own to fall back on.
import { readFileSync, writeFileSync } from 'node:fs';
import { expect, test, type BrowserContext, type CDPSession, type Page } from '@playwright/test';

type World = {
  address: string; // host:port of the edge, which serves auth.example.test, admin.example.test and the app's name
  spki: string; // its certificate's public key hash, which Chromium takes as trusted
  enrolment: string;
  admin: string; // an authorize URL of Admin's client
  landing: string; // where Admin's client sends the person back
  app: string; // https://recipes.example.test, the app's one URL
  username: string;
  name: string; // the person's name, as the app has it
  mail: string;
  out: string; // where the browser says it is done, for the suite
};

const file = process.env.HOMERSLAB_TEST_NATIVE;
const world: World | null = file ? (JSON.parse(readFileSync(file, 'utf8')) as World) : null;

test.skip(!world, 'run by the identity provider contract suite (HOMERSLAB_TEST_NATIVE)');
test.describe.configure({ mode: 'serial', timeout: 180_000 });

test.use({
  ignoreHTTPSErrors: false,
  launchOptions: world
    ? { args: [`--host-resolver-rules=MAP *.example.test:443 ${world.address}`, `--ignore-certificate-errors-spki-list=${world.spki}`] }
    : {},
});

async function authenticator(context: BrowserContext, page: Page): Promise<{ cdp: CDPSession; id: string }> {
  const cdp = await context.newCDPSession(page);
  await cdp.send('WebAuthn.enable', { enableUI: false });
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true,
      isUserVerified: true, automaticPresenceSimulation: true },
  });
  return { cdp, id: authenticatorId };
}

// signInAtAdmin is the person's one sign-in, at Admin: what comes before what this file checks. Authentik's passkey
// stage sometimes issues a new challenge while the page is answering the one before ("Client data challenge was not
// expected challenge", over and over, in CI and on a desktop alike), so a sign-in that hasn't landed in 20 seconds is
// started again, from Admin's authorize URL, up to three times. Opening the app afterwards is never retried.
async function signInAtAdmin(page: Page, w: World): Promise<void> {
  for (let attempt = 1; ; attempt++) {
    await page.goto(w.admin);
    const user = page.getByRole('textbox', { name: 'Email or Username' });
    if (await user.waitFor({ timeout: 15_000 }).then(() => true, () => false)) {
      await user.fill(w.username);
      await page.getByRole('button', { name: 'Log in' }).click();
    }
    try {
      await page.waitForURL((u) => u.href.startsWith(w.landing), { timeout: 20_000 });
      return;
    } catch (e) {
      if (attempt === 3) throw e;
    }
  }
}

async function signCount(cdp: CDPSession, id: string): Promise<number> {
  const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId: id });
  expect(credentials).toHaveLength(1);
  return credentials[0]?.signCount ?? 0;
}

test('signed in to the household account once, a person opens the app and is in it, with no second login', async ({ browser }) => {
  const w = world as World;
  const context = await browser.newContext();
  const page = await context.newPage();
  const { cdp, id } = await authenticator(context, page);

  // The person's passkey, from their enrolment link; then they sign in once, at Admin.
  await page.goto(w.enrolment);
  await page.waitForURL((u) => u.href.startsWith(w.landing), { timeout: 60_000 });
  await signInAtAdmin(page, w);
  const signedIn = await signCount(cdp, id);

  // Then the app: through the sign-in and back, without a page that asks for anything.
  const shown: string[] = [];
  page.on('framenavigated', (f) => { if (f === page.mainFrame()) shown.push(f.url()); });
  await page.goto(w.app);
  // Mealie's front end sends the browser to its /login, which goes on to the sign-in and back: done once the app
  // says who is signed in. Asked from the page, so the browser's own cookies and names are used; a navigation in
  // between destroys the page's context, which counts as not yet.
  const whoAmI = async () => {
    try {
      return await page.evaluate(async () => {
        const r = await fetch('/api/users/self');
        return { status: r.status, body: r.status === 200 ? ((await r.json()) as Record<string, unknown>) : null };
      });
    } catch {
      return { status: 0, body: null };
    }
  };
  let self = { status: 0, body: null as Record<string, unknown> | null };
  await expect
    .poll(async () => ((self = await whoAmI()), self.status), { timeout: 90_000, intervals: [1_000] })
    .toBe(200);
  expect(new URL(page.url()).origin).toBe(new URL(w.app).origin);
  // Signed in as themselves, made by the sign-in (no account of the app's own), an admin as their group says.
  expect(self.body).toMatchObject({ email: w.mail, fullName: w.name, authMethod: 'OIDC', admin: true });
  expect(await signCount(cdp, id)).toBe(signedIn); // the passkey wasn't asked for again
  expect(shown.filter((u) => new URL(u).pathname.startsWith('/if/flow/'))).toEqual([]); // no sign-in page shown
  await context.close();
});

test('the app has no password sign-in of its own', async ({ browser }) => {
  const w = world as World;
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto(`${w.app}/login?direct=1`);
  await expect(page.getByRole('textbox', { name: /password/i })).toHaveCount(0);
  await context.close();
  writeFileSync(w.out, 'in, with no second login\n');
});
