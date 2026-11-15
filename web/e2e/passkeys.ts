// Chromium's virtual authenticator, for the specs that sign in to a real Authentik (*.idp.spec.ts), as a person's
// device behaves: it answers only after the page's own requests to the sign-in's flow are answered.
//
// Why (#222): Authentik's passkey stage, when the person has one passkey, picks it on the page and tells the server so
// in a request of its own that it doesn't wait for (the stage's `selectedChallenge`). Every request to the flow saves
// the flow's state in the session, as it read it. A person's touch comes long after that request is answered; the
// virtual authenticator answers at once, so the passkey's answer and that request reach Authentik together. When the
// selection's request is saved last, it puts back the flow as it was before the passkey was accepted: the flow asks for
// the passkey again with a new challenge, while the page keeps answering the old one ("Client data challenge was not
// expected challenge", over and over, until the test times out).
//
// So the authenticator's answer goes to Authentik only once every request to the flow that the page sent before it has
// been answered. The answer itself is never changed or made up: Authentik still checks it as it would a device's.
import type { BrowserContext, CDPSession, Page, Request } from '@playwright/test';

const flowAPI = '/api/v3/flows/executor/';

function toTheFlow(url: string): boolean {
  return new URL(url).pathname.startsWith(flowAPI);
}

// A request that carries a passkey's answer (an assertion or an attestation): WebAuthn's client data is in every one.
function carriesAPasskey(r: Request): boolean {
  return r.method() === 'POST' && (r.postData() ?? '').includes('clientDataJSON');
}

// asAPersonWould holds each passkey answer to the flow until the flow requests sent before it are answered. Called once
// per context, before its first page goes to the sign-in.
export async function asAPersonWould(context: BrowserContext): Promise<void> {
  let sent = 0;
  const order = new WeakMap<Request, number>();
  const open = new Map<Request, number>();
  context.on('request', (r) => {
    if (!toTheFlow(r.url())) return;
    order.set(r, ++sent);
    open.set(r, sent);
  });
  const closed = (r: Request) => open.delete(r);
  context.on('requestfinished', closed);
  context.on('requestfailed', closed);
  await context.route((u) => u.pathname.startsWith(flowAPI), async (route) => {
    const r = route.request();
    if (carriesAPasskey(r)) {
      const mine = order.get(r) ?? Number.MAX_SAFE_INTEGER;
      const deadline = Date.now() + 30_000;
      while ([...open.values()].some((n) => n < mine) && Date.now() < deadline) {
        await new Promise((done) => setTimeout(done, 25));
      }
    }
    await route.continue();
  });
}

// authenticator puts a virtual authenticator on the page: a platform one with resident keys that verifies the person,
// or (verifies false) one that only checks presence, a touch alone.
export async function authenticator(context: BrowserContext, page: Page, verifies = true): Promise<{ cdp: CDPSession; id: string }> {
  const cdp = await context.newCDPSession(page);
  await cdp.send('WebAuthn.enable', { enableUI: false });
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true,
      isUserVerified: verifies, automaticPresenceSimulation: true },
  });
  return { cdp, id: authenticatorId };
}
