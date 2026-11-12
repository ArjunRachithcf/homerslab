// First-time setup's side of the manager's API, on the setup listener. Claiming the link gives a session token, which
// this tab keeps (sessionStorage) and sends as a bearer token: not a cookie, which every port of this host would get.
// That token is the one thing the tab stores. The link's key, and the tab's nonce for its claim, are held only in
// memory and in the address's fragment until the manager has answered the claim; the key never goes anywhere but the
// claim's body.

const TOKEN = 'homerslab.setup.session';
const memory = new Map<string, string>(); // when the browser refuses storage, and the newest value

function read(name: string): string | null {
  try {
    return memory.get(name) ?? sessionStorage.getItem(name); // what this page just kept wins over an older stored value
  } catch {
    return memory.get(name) ?? null;
  }
}

function write(name: string, value: string) {
  memory.set(name, value);
  try {
    sessionStorage.setItem(name, value);
  } catch {
    // memory holds it for this page's life
  }
}

const token = () => read(TOKEN);

/**
 * A link's claim, as this tab sends it: the link's key, and the tab's own random nonce. The manager answers the same
 * key and nonce from the same address with the same session for a minute after it took the claim, so a claim whose
 * answer was lost, or a reload while it was out, can be sent again; anyone else is refused.
 */
export type Link = { key: string; nonce: string };

let held: Link | null = null;

/** The link this tab holds until the manager has answered its claim: in memory only. */
export const heldLink = () => held;
export const holdLink = (link: Link) => { held = link; };
export const dropLink = () => { held = null; };

/** A nonce as the manager takes it (goodNonce in internal/manager/setup): 22 to 128 URL-safe base64 characters. */
export const goodNonce = (n: string | null): n is string => n !== null && /^[A-Za-z0-9_-]{22,128}$/.test(n);

/** A new nonce: 256 random bits. */
export function newNonce(): string {
  const b = crypto.getRandomValues(new Uint8Array(32));
  return btoa(String.fromCharCode(...b)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export type Half = 'lan' | 'mesh';
export type StepState = { id: string; half: Half; state: 'todo' | 'waiting' | 'done'; secret?: string; command?: string; confirm?: string };
export type Setup = { half: Half; current: string; steps: StepState[]; answers: Record<string, string> };

export type Facts = {
  hostname: string;
  distro: { id: string; name: string; version: string; pretty: string };
  architecture: string;
  memoryBytes: number;
  disks: { name: string; model: string; sizeBytes: number; rotational: boolean; removable: boolean }[];
  tpm: { present: boolean; version?: string };
  gpus: { vendor: string; model: string; pciId: string }[];
};

/** Why the manager refused a field's answer. */
export type Reason = 'missing' | 'too-long' | 'hidden-characters' | 'too-many-marks' | 'not-a-mail';

/** The request got no answer: the network dropped it, or the manager isn't listening. */
export class Unreachable extends Error {
  constructor() {
    super('unreachable');
  }
}

/** The manager said no, with its reason: for an invalid answer, each refused field and why. */
export class Refused extends Error {
  constructor(readonly status: number, readonly code: string, readonly reasons: Record<string, Reason> = {}) {
    super(code);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const t = token();
  if (t) headers.Authorization = `Bearer ${t}`;
  let resp: Response;
  try {
    resp = await fetch(path, {
      method,
      credentials: 'omit',
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new Unreachable();
  }
  if (resp.status === 204) return undefined as T;
  const data: unknown = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    const e = data as { error?: string; fields?: string[]; reasons?: Record<string, Reason> };
    const reasons = { ...Object.fromEntries((e.fields ?? []).map((f) => [f, 'missing' as Reason])), ...e.reasons };
    throw new Refused(resp.status, e.error ?? 'unknown', reasons);
  }
  return data as T;
}

const claiming = new Map<string, Promise<string>>();

/** Claims the link, and gives its session token. The same link, claimed again while its claim is out, shares it. */
export function claim({ key, nonce }: Link): Promise<string> {
  const id = `${key} ${nonce}`;
  let claimed = claiming.get(id);
  if (!claimed) {
    claimed = call<{ token: string }>('POST', '/api/v1/setup/claim', { key, nonce }).then((r) => r.token)
      .finally(() => claiming.delete(id));
    claiming.set(id, claimed);
  }
  return claimed;
}

/** Keeps the session a claim opened: this tab's, for every later request. */
export const keepSession = (token: string) => write(TOKEN, token);
export const getSetup = () => call<Setup>('GET', '/api/v1/setup');
export const getPrimary = () => call<Facts>('GET', '/api/v1/nodes/primary');
export const answer = (step: string, body: Record<string, string | boolean>) => call<Setup>('PUT', `/api/v1/setup/steps/${encodeURIComponent(step)}`, body);
